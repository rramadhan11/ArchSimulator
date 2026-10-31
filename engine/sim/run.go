// Package sim is the simulation engine. Both modes share one compiled graph:
//
//   - Run is the quick check: a steady-state queueing-network model (M/M/c
//     with Kingman correction, USL contention, steady-state autoscaling and pod
//     scheduling) plus a seeded Monte Carlo pass for latency percentiles.
//   - Simulate is the time simulation: the same stations stepped once per
//     second with fluid backlogs, autoscaler control loops and provisioning
//     delays (see timeline.go).
package sim

import (
	"fmt"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

const (
	// maxSearchTPS stops the max-TPS search for designs with no finite bottleneck.
	maxSearchTPS = 1e9
	// minSearchTPS is the resolution below which a design counts as unable to
	// sustain any load.
	minSearchTPS = 0.01
	// searchPrecision is the relative width at which bisection stops.
	searchPrecision = 0.002
	// saturationTop is how many resources the saturation order lists.
	saturationTop = 5
)

// Run evaluates the design at its target TPS and searches for the maximum
// sustainable TPS. Results are deterministic for a given design and seed.
func Run(a *model.Architecture, cat *catalog.Catalog) (*Result, error) {
	g, err := compile(a, cat)
	if err != nil {
		return nil, err
	}
	g.checkCatalogVersion()
	w := a.Workload
	samples, seed := w.SampleCount(), w.RandSeed()

	target := g.evaluate(w.TargetTPS, samples, seed)
	// The search runs many evaluations; a smaller sample keeps it fast while
	// the shared seed keeps neighbouring evaluations comparable.
	maxTPS, limitedBy, atMax := g.searchMax(w.TargetTPS, max(samples/4, 2000), seed)

	order := atMax.Resources
	if len(order) > saturationTop {
		order = order[:saturationTop]
	}
	return &Result{
		CatalogVersion:    cat.Version,
		Target:            *target,
		MaxSustainableTPS: maxTPS,
		LimitedBy:         limitedBy,
		SaturationOrder:   order,
	}, nil
}

func (g *graph) checkCatalogVersion() {
	if v := g.a.CatalogVersion; v != "" && v != g.cat.Version {
		g.issues = append(g.issues, Issue{Severity: SeverityWarning, Ref: "catalogVersion",
			Message: fmt.Sprintf("design was made with catalog %s; evaluated with %s, defaults may differ", v, g.cat.Version)})
	}
}

// searchMax finds the largest feasible TPS by exponential search followed by
// bisection, which assumes feasibility is monotone in TPS. That holds for fixed
// capacity; with autoscaling, utilization dips after each replica step but an
// autoscaled resource only becomes infeasible once it is pinned at max.
func (g *graph) searchMax(start float64, samples int, seed uint64) (*float64, string, *Report) {
	lo, hi := 0.0, start
	var loRep *Report
	hiRep := g.evaluate(hi, samples, seed)
	for hiRep.feasible {
		lo, loRep = hi, hiRep
		if hi >= maxSearchTPS {
			return nil, "none", loRep
		}
		hi *= 2
		hiRep = g.evaluate(hi, samples, seed)
	}
	for hi-lo > searchPrecision*hi && hi > minSearchTPS {
		mid := (lo + hi) / 2
		if r := g.evaluate(mid, samples, seed); r.feasible {
			lo, loRep = mid, r
		} else {
			hi, hiRep = mid, r
		}
	}
	if loRep == nil {
		return ptr(0), hiRep.limitedBy, hiRep
	}
	return ptr(lo), hiRep.limitedBy, loRep
}
