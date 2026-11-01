// Command archsim runs the simulation engine from the command line.
//
//	archsim run [--tps N] [--json] design.json
//	archsim simulate [--every S] [--json] design.json
//	archsim catalog [--json]
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"archsim/engine/catalog"
	"archsim/engine/model"
	"archsim/engine/sim"
)

const usage = `usage:
  archsim run [--tps N] [--json] design.json         quick check: verdict, bottlenecks, max TPS
  archsim simulate [--every S] [--json] design.json  time simulation (workload.timeline): autoscaling, backlog
  archsim catalog [--json]                           list catalog components (--json: full catalog)
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "archsim:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cat, err := catalog.Load()
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	switch args[0] {
	case "run":
		return runDesign(args[1:], cat, out)
	case "simulate":
		return simulateDesign(args[1:], cat, out)
	case "catalog":
		return listCatalog(args[1:], cat, out)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runDesign(args []string, cat *catalog.Catalog, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	tps := fs.Float64("tps", 0, "override workload.targetTps")
	asJSON := fs.Bool("json", false, "print the full result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(usage)
	}
	a, err := loadDesign(fs.Arg(0))
	if err != nil {
		return err
	}
	if *tps > 0 {
		a.Workload.TargetTPS = *tps
	}
	res, err := sim.Run(a, cat)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(out, res)
	}
	printResult(out, res)
	return nil
}

func simulateDesign(args []string, cat *catalog.Catalog, out io.Writer) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	every := fs.Int("every", 10, "print one row every S seconds")
	asJSON := fs.Bool("json", false, "print the full timeline as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *every < 1 {
		return errors.New(usage)
	}
	a, err := loadDesign(fs.Arg(0))
	if err != nil {
		return err
	}
	tl, err := sim.Simulate(a, cat)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(out, tl)
	}
	printTimeline(out, tl, *every)
	return nil
}

func loadDesign(path string) (*model.Architecture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return model.Parse(data)
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func listCatalog(args []string, cat *catalog.Catalog, out io.Writer) error {
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the full catalog as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(out, cat)
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "KIND\tARCHETYPE\tNAME\n")
	for _, id := range cat.ComponentIDs() {
		c := cat.Components[id]
		fmt.Fprintf(tw, "%s\t%s\t%s\n", id, c.Archetype, c.DisplayName)
	}
	return tw.Flush()
}

var verdictLabel = map[sim.Verdict]string{
	sim.VerdictSafe:     "SAFE",
	sim.VerdictWarning:  "WARNING",
	sim.VerdictOverload: "OVERLOAD",
}

func printResult(out io.Writer, res *sim.Result) {
	t := res.Target
	fmt.Fprintf(out, "Verdict at %.0f TPS: %s\n", t.TPS, verdictLabel[t.Verdict])
	for _, r := range t.Reasons {
		fmt.Fprintf(out, "  - %s\n", r)
	}
	if t.Latency != nil {
		fmt.Fprintf(out, "Latency: mean %.1f ms · p50 %.1f · p95 %.1f · p99 %.1f ms · errors %.3f%%\n",
			t.Latency.MeanMs, t.Latency.P50Ms, t.Latency.P95Ms, t.Latency.P99Ms, *t.ErrorRate*100)
	} else {
		fmt.Fprintln(out, "Latency: unbounded (system is unstable at this load)")
	}
	if res.MaxSustainableTPS != nil {
		fmt.Fprintf(out, "Max sustainable TPS: %.0f (limited by %s)\n", *res.MaxSustainableTPS, res.LimitedBy)
	} else {
		fmt.Fprintln(out, "Max sustainable TPS: no finite bottleneck found")
	}

	fmt.Fprintln(out, "\nResources at target:")
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "  RESOURCE\tUTIL\tLOAD\tCAPACITY\tUNIT\n")
	for _, r := range t.Resources {
		fmt.Fprintf(tw, "  %s\t%s\t%.0f\t%.0f\t%s\n", r.ID, util(r), r.Load, r.Capacity, r.Unit)
	}
	tw.Flush()

	if len(res.SaturationOrder) > 0 {
		names := make([]string, 0, len(res.SaturationOrder))
		for _, r := range res.SaturationOrder {
			names = append(names, fmt.Sprintf("%s (%s)", r.ID, util(r)))
		}
		fmt.Fprintf(out, "\nSaturates first at max TPS: %s\n", strings.Join(names, " > "))
	}

	if len(t.Hops) > 0 {
		fmt.Fprintln(out, "\nLatency breakdown per request (mean ms):")
		tw = tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "  NODE\tVISITS\tNETWORK\tWAIT\tSERVICE\tDELAY\n")
		for _, h := range t.Hops {
			fmt.Fprintf(tw, "  %s\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\n", h.Node, h.VisitsPerRequest, h.NetworkMs, h.WaitMs, h.ServiceMs, h.DelayMs)
		}
		tw.Flush()
	}
	for _, a := range t.Async {
		fmt.Fprintf(out, "Async %s: processing p50 %.1f ms · p99 %.1f ms\n", a.Edge, a.P50Ms, a.P99Ms)
	}

	if len(t.Replicas) > 0 {
		fmt.Fprintln(out, "\nReplicas:")
		for _, r := range t.Replicas {
			line := fmt.Sprintf("  %s: %d desired, %d scheduled", r.Service, r.Desired, r.Scheduled)
			if r.Autoscaler != "" {
				line += fmt.Sprintf(" (%s %d..%d)", r.Autoscaler, r.Min, r.Max)
			}
			if r.Pending > 0 {
				line += fmt.Sprintf(", %d pending", r.Pending)
			}
			fmt.Fprintln(out, line)
		}
	}
	for _, p := range t.NodePools {
		fmt.Fprintf(out, "  node pool %s: %d/%d nodes needed\n", p.Node, p.NodesNeeded, p.NodesAvailable)
	}

	if len(t.Issues) > 0 {
		fmt.Fprintln(out, "\nIssues:")
		for _, is := range t.Issues {
			fmt.Fprintf(out, "  [%s] %s: %s\n", is.Severity, is.Ref, is.Message)
		}
	}
	fmt.Fprintf(out, "\nConfidence: %s (%.2f). %s\n", t.Confidence.Level, t.Confidence.Score, t.Confidence.Summary)
}

func util(r sim.ResourceReport) string {
	if r.Utilization == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", *r.Utilization*100)
}

func printTimeline(out io.Writer, tl *sim.Timeline, every int) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "T(s)\tTPS\tP99(ms)\tERRORS\t")
	for _, s := range tl.Services {
		fmt.Fprintf(tw, "%s\t", s)
	}
	for _, e := range tl.Edges {
		fmt.Fprintf(tw, "%s\t", e)
	}
	fmt.Fprintf(tw, "BUSIEST\t\n")
	for i, tk := range tl.Ticks {
		if i%every != 0 && i != len(tl.Ticks)-1 {
			continue
		}
		p99 := "-"
		if tk.Latency != nil {
			p99 = fmt.Sprintf("%.0f", tk.Latency.P99Ms)
		}
		fmt.Fprintf(tw, "%.0f\t%.0f\t%s\t%.2f%%\t", tk.T, tk.OfferedTPS, p99, tk.ErrorRate*100)
		for _, r := range tk.Replicas {
			fmt.Fprintf(tw, "%d/%d\t", r.Ready, r.Desired)
		}
		for _, e := range tk.Edges {
			fmt.Fprintf(tw, "%.0f/s %s\t", e.Attempts, e.Breaker)
		}
		busiest, load := "", -1.0
		for k, st := range tk.Stations {
			if st.Load != nil && *st.Load > load {
				busiest, load = tl.Stations[k], *st.Load
			}
		}
		fmt.Fprintf(tw, "%s %.0f%%\t\n", busiest, load*100)
	}
	tw.Flush()
	fmt.Fprintln(out, "(replicas shown as ready/desired; edges as attempts/s incl. retries and breaker state)")
	if len(tl.Failures) > 0 {
		fmt.Fprintln(out, "\nInjected failures:")
		for _, f := range tl.Failures {
			end := "end of run"
			if f.EndS != nil {
				end = fmt.Sprintf("t=%.0f", *f.EndS)
			}
			fmt.Fprintf(out, "  t=%-5.0f %s %s (until %s)\n", f.StartS, f.Target, f.Label, end)
		}
	}

	if len(tl.Events) > 0 {
		fmt.Fprintln(out, "\nEvents:")
		for _, e := range tl.Events {
			fmt.Fprintf(out, "  t=%-5.0f %s: %s\n", e.T, e.Ref, e.Message)
		}
	}
	sum := tl.Summary
	fmt.Fprintln(out, "\nSummary:")
	if sum.PeakP99Ms != nil {
		fmt.Fprintf(out, "  peak p99 %.0f ms · max error rate %.2f%%\n", *sum.PeakP99Ms, sum.MaxErrorRate*100)
	}
	if sum.FirstBreachS != nil {
		fmt.Fprintf(out, "  SLO broken for %.0f s, first at t=%.0f s\n", sum.SLOBreachS, *sum.FirstBreachS)
	} else {
		fmt.Fprintln(out, "  SLO held for the whole run")
	}
	for _, is := range tl.Issues {
		fmt.Fprintf(out, "  [%s] %s: %s\n", is.Severity, is.Ref, is.Message)
	}
}
