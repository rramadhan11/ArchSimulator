// Command analyze compares a calibration run with ArchSim's prediction.
//
//	go run ./lab/cmd/analyze <results dir>
//
// The directory holds k6.csv (k6 --out csv), pods.csv (unix seconds, ready,
// desired replicas; written by run.sh) and sim.json (archsim simulate --json
// of lab/design.json). Time 0 is k6's first request.
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"text/tabwriter"
)

// second is what was observed (or simulated) during one second of the run.
type second struct {
	requests, failed float64
	durations        []float64 // ms, failed requests included (as ArchSim does)
	p99              *float64
	ready, desired   int
	hasPods          bool
	dropped          float64 // k6 could not start these iterations: offered load fell short
}

// sim mirrors the parts of archsim's Timeline JSON used here.
type sim struct {
	Ticks []struct {
		T          float64 `json:"t"`
		OfferedTPS float64 `json:"offeredTps"`
		ErrorRate  float64 `json:"errorRate"`
		Latency    *struct {
			P99Ms float64 `json:"p99Ms"`
		} `json:"latency"`
		Replicas []struct {
			Desired int `json:"desired"`
			Ready   int `json:"ready"`
		} `json:"replicas"`
	} `json:"ticks"`
}

const (
	window = 10  // seconds per row of the table
	sloP99 = 250 // ms, as in lab/design.json
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: analyze <results dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		os.Exit(1)
	}
}

func run(dir string, out io.Writer) error {
	obs, t0, err := readK6(filepath.Join(dir, "k6.csv"))
	if err != nil {
		return err
	}
	if err := readPods(filepath.Join(dir, "pods.csv"), t0, obs); err != nil {
		return err
	}
	pred, err := readSim(filepath.Join(dir, "sim.json"))
	if err != nil {
		return err
	}
	n := min(len(obs), len(pred))
	if n == 0 {
		return errors.New("no overlapping seconds between the run and the simulation")
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "T(s)\treq/s\tP99 real\tP99 sim\terr real\terr sim\tpods real\tpods sim\t")
	for from := 0; from < n; from += window {
		to := min(from+window, n)
		var req, failed, simErr, simReq float64
		var p99Real, p99Sim *float64
		for i := from; i < to; i++ {
			req += obs[i].requests
			failed += obs[i].failed
			p99Real = maxPtr(p99Real, obs[i].p99)
			simReq += pred[i].requests
			simErr += pred[i].failed
			p99Sim = maxPtr(p99Sim, pred[i].p99)
		}
		last := to - 1
		fmt.Fprintf(tw, "%d\t%.0f\t%s\t%s\t%s\t%s\t%s\t%d/%d\t\n", from, req/float64(to-from),
			ms(p99Real), ms(p99Sim), pct(failed, req), pct(simErr, simReq),
			pods(obs[last]), pred[last].ready, pred[last].desired)
	}
	tw.Flush()
	fmt.Fprintln(out, "(P99: worst second in the row; pods: ready/desired at the row's last second)")

	fmt.Fprintln(out, "\nMilestones                         real        sim")
	milestone := func(name string, f func([]second) string) {
		fmt.Fprintf(out, "  %-30s %10s %10s\n", name, f(obs[:n]), f(pred[:n]))
	}
	milestone("first scale-up decision", func(s []second) string { return at(firstScaleUp(s)) })
	milestone("peak ready pods", peakPods)
	milestone("peak p99", peakP99)
	milestone("p99 back under SLO for good", func(s []second) string { return at(recovered(s)) })
	milestone("errors over the run", func(s []second) string {
		var req, failed float64
		for _, x := range s {
			req += x.requests
			failed += x.failed
		}
		return pct(failed, req)
	})

	dropped := 0.0
	for _, x := range obs {
		dropped += x.dropped
	}
	if dropped > 0 {
		fmt.Fprintf(out, "\nWarning: k6 dropped %.0f iterations (not enough VUs): the real offered load was lower than the simulated one.\n", dropped)
	}
	return nil
}

// readK6 aggregates k6's CSV output per second.
func readK6(path string) ([]second, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, c := range []string{"metric_name", "timestamp", "metric_value"} {
		if _, ok := col[c]; !ok {
			return nil, 0, fmt.Errorf("%s: missing column %q", path, c)
		}
	}

	type row struct {
		metric string
		ts     int64
		value  float64
	}
	var rows []row
	t0 := int64(math.MaxInt64)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", path, err)
		}
		ts, err1 := strconv.ParseInt(rec[col["timestamp"]], 10, 64)
		v, err2 := strconv.ParseFloat(rec[col["metric_value"]], 64)
		if err1 != nil || err2 != nil {
			return nil, 0, fmt.Errorf("%s: bad row %v", path, rec)
		}
		m := rec[col["metric_name"]]
		if m == "http_reqs" {
			t0 = min(t0, ts)
		}
		rows = append(rows, row{m, ts, v})
	}
	if t0 == math.MaxInt64 {
		return nil, 0, fmt.Errorf("%s: no requests recorded", path)
	}

	var out []second
	get := func(ts int64) *second {
		i := int(ts - t0)
		for len(out) <= i {
			out = append(out, second{})
		}
		return &out[i]
	}
	for _, x := range rows {
		if x.ts < t0 {
			continue
		}
		s := get(x.ts)
		switch x.metric {
		case "http_reqs":
			s.requests += x.value
		case "http_req_failed":
			s.failed += x.value
		case "http_req_duration":
			s.durations = append(s.durations, x.value)
		case "dropped_iterations":
			s.dropped += x.value
		}
	}
	for i := range out {
		if d := out[i].durations; len(d) > 0 {
			slices.Sort(d)
			out[i].p99 = ptr(d[min(len(d)-1, int(math.Ceil(0.99*float64(len(d))))-1)])
		}
	}
	return out, t0, nil
}

// readPods fills in replica counts sampled by run.sh ("unix,ready,desired";
// ready is empty while no pod is ready).
func readPods(path string, t0 int64, obs []second) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) != 3 {
			return fmt.Errorf("%s: bad row %v (%v)", path, rec, err)
		}
		ts, err := strconv.ParseInt(rec[0], 10, 64)
		if err != nil {
			return fmt.Errorf("%s: bad time %q", path, rec[0])
		}
		i := int(ts - t0)
		if i < 0 || i >= len(obs) {
			continue
		}
		ready, _ := strconv.Atoi(rec[1]) // empty = 0
		desired, err := strconv.Atoi(rec[2])
		if err != nil {
			return fmt.Errorf("%s: bad desired %q", path, rec[2])
		}
		obs[i].ready, obs[i].desired, obs[i].hasPods = ready, desired, true
	}
	// Seconds the watcher missed (kubectl is slower than 1/s under load)
	// take the previous sample.
	for i := 1; i < len(obs); i++ {
		if !obs[i].hasPods && obs[i-1].hasPods {
			obs[i].ready, obs[i].desired, obs[i].hasPods = obs[i-1].ready, obs[i-1].desired, true
		}
	}
	return nil
}

func readSim(path string) ([]second, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s sim
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]second, len(s.Ticks))
	for i, tk := range s.Ticks {
		out[i] = second{requests: tk.OfferedTPS, failed: tk.OfferedTPS * tk.ErrorRate, hasPods: true}
		if tk.Latency != nil {
			out[i].p99 = ptr(tk.Latency.P99Ms)
		}
		if len(tk.Replicas) > 0 {
			out[i].ready, out[i].desired = tk.Replicas[0].Ready, tk.Replicas[0].Desired
		}
	}
	return out, nil
}

func firstScaleUp(s []second) int {
	for i := 1; i < len(s); i++ {
		if s[i].hasPods && s[0].hasPods && s[i].desired > s[0].desired {
			return i
		}
	}
	return -1
}

func peakPods(s []second) string {
	best, when := -1, -1
	for i, x := range s {
		if x.hasPods && x.ready > best {
			best, when = x.ready, i
		}
	}
	if when < 0 {
		return "–"
	}
	return fmt.Sprintf("%d @ %ds", best, when)
}

func peakP99(s []second) string {
	var best *float64
	when := -1
	for i, x := range s {
		if x.p99 != nil && (best == nil || *x.p99 > *best) {
			best, when = x.p99, i
		}
	}
	if best == nil {
		return "–"
	}
	return fmt.Sprintf("%s @ %ds", ms(best), when)
}

// recovered is the second after the last one whose p99 broke the SLO.
func recovered(s []second) int {
	last := -1
	for i, x := range s {
		if x.p99 == nil || *x.p99 > sloP99 {
			last = i
		}
	}
	if last < 0 || last+1 >= len(s) {
		return -1
	}
	return last + 1
}

func at(i int) string {
	if i < 0 {
		return "–"
	}
	return fmt.Sprintf("t=%ds", i)
}

func pods(s second) string {
	if !s.hasPods {
		return "–"
	}
	return fmt.Sprintf("%d/%d", s.ready, s.desired)
}

func ms(p *float64) string {
	if p == nil {
		return "–"
	}
	return fmt.Sprintf("%.0fms", *p)
}

func pct(part, whole float64) string {
	if whole == 0 {
		return "–"
	}
	return fmt.Sprintf("%.2f%%", part/whole*100)
}

func maxPtr(a, b *float64) *float64 {
	if a == nil || (b != nil && *b > *a) {
		return b
	}
	return a
}

func ptr(f float64) *float64 { return &f }
