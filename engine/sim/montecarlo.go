package sim

import (
	"math"
	"sort"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// The Monte Carlo pass samples synthetic requests through the call trees,
// drawing a queueing wait and a service time at every station from its
// steady-state distribution. It yields end-to-end percentiles, a per-hop
// breakdown, and mean holding times for the Little's-law resources.

type mcAcc struct {
	main                 bool // false while sampling async pipelines
	wait, service, delay []float64
	net                  []float64 // per edge
	resid, residN        []float64 // per node: total time held incl. downstream
	callMs, callN        []float64 // per edge
}

type mcResult struct {
	latency   LatencyStats
	errorRate float64
	hops      []HopReport
	async     []AsyncReport
	acc       *mcAcc
}

func (m *mcResult) meanResidence(node int) float64 {
	if m.acc.residN[node] == 0 {
		return 0
	}
	return m.acc.resid[node] / m.acc.residN[node]
}

func (m *mcResult) meanCallMs(edge int) float64 {
	if m.acc.callN[edge] == 0 {
		return 0
	}
	return m.acc.callMs[edge] / m.acc.callN[edge]
}

// monteCarlo samples requests through the call trees; reject[n] is the
// probability that node n refuses a visit. withAsync also samples the async
// pipelines (skipped by the time simulation, which tracks them as backlog).
func (g *graph) monteCarlo(samples int, seed uint64, reject []float64, withAsync bool) *mcResult {
	s := newSampler(seed)
	nn, ne := len(g.nodes), len(g.edges)
	acc := &mcAcc{
		main: true,
		wait: make([]float64, nn), service: make([]float64, nn), delay: make([]float64, nn),
		resid: make([]float64, nn), residN: make([]float64, nn),
		net: make([]float64, ne), callMs: make([]float64, ne), callN: make([]float64, ne),
	}

	cum := make([]float64, len(g.weights))
	total := 0.0
	for i, w := range g.weights {
		total += w
		cum[i] = total
	}
	durations := make([]float64, samples)
	errors := 0
	for i := range durations {
		u := s.r.Float64() * total
		class := sort.SearchFloat64s(cum, u)
		if class >= len(cum) {
			class = len(cum) - 1
		}
		d, failed := g.sample(g.trees[class], s, acc, reject)
		durations[i] = d
		if failed {
			errors++
		}
	}
	res := &mcResult{latency: summarize(durations), errorRate: float64(errors) / float64(samples), acc: acc}
	res.hops = g.hops(acc, float64(samples))

	if withAsync {
		acc.main = false
		res.async = g.sampleAsync(s, acc, reject, max(samples/4, 1000))
	}
	return res
}

func (g *graph) sample(v *visit, s *sampler, acc *mcAcc, reject []float64) (float64, bool) {
	n := v.node.idx
	lv := g.live
	if lv != nil {
		mode := lv.down[n]
		if mode == "" && v.st != nil && v.st == v.node.st {
			mode = lv.primaryDown[n]
		}
		switch mode {
		case model.ModeError:
			return 0, true
		case model.ModeHang:
			return lv.hangMs, true
		}
	}
	if s.bernoulli(reject[n]) {
		return 0, true
	}
	t := 0.0
	if st := v.st; st != nil && st.meanS > 0 {
		// Requests arriving during a tick see the backlog grow (or drain)
		// from its level at the start to its level at the end.
		w := s.wait(st.q) + st.fluidLoMs
		if st.fluidMs != st.fluidLoMs {
			w += s.r.Float64() * (st.fluidMs - st.fluidLoMs)
		}
		svc := s.gamma(v.serviceMs/st.speed, v.cs2)
		if acc.main {
			acc.wait[n] += w
			acc.service[n] += svc
		}
		t += w + svc
	}
	d := s.gamma(v.delayMs, v.delayCs2)
	if lv != nil {
		d += lv.extraMs[n]
	}
	if acc.main {
		acc.delay[n] += d
	}
	t += d

	failed := false
	for _, grp := range v.groups {
		span := 0.0
		for _, c := range grp {
			if c.e.async {
				continue
			}
			ct, f := g.sampleCall(c, s, acc, reject)
			span = math.Max(span, ct)
			failed = failed || f
		}
		t += span
		if failed {
			break // the request errors out; later calls are not made
		}
	}
	acc.resid[n] += t
	acc.residN[n]++
	return t, failed
}

// sampleCall draws all calls made over one edge during one visit of the caller.
func (g *graph) sampleCall(c *call, s *sampler, acc *mcAcc, reject []float64) (float64, bool) {
	total := 0.0
	for range s.count(c.e.calls) {
		if !s.bernoulli(c.e.prob) {
			continue
		}
		d, failed := g.sampleAttempts(c, s, acc, reject)
		acc.callMs[c.e.idx] += d
		acc.callN[c.e.idx]++
		if c.parallel {
			total = math.Max(total, d)
		} else {
			total += d
		}
		if failed {
			return total, true
		}
	}
	return total, false
}

// sampleAttempts makes one call over an edge. In the time simulation that
// includes the circuit breaker's verdict and the retries of failed attempts.
func (g *graph) sampleAttempts(c *call, s *sampler, acc *mcAcc, reject []float64) (float64, bool) {
	e, lv := c.e, g.live
	elapsed := 0.0
	for k := 0; ; k++ {
		pass := 1.0
		if lv != nil {
			pass = lv.pass[e.idx]
		}
		if pass == 0 {
			return elapsed, true // breaker open: fail fast, not retried
		}
		d, failed := g.sample(c.child, s, acc, reject)
		d += e.netMs
		if lv != nil {
			d += lv.extraNetMs[e.idx]
		}
		if to := e.m.TimeoutMs; to > 0 && d > to {
			d, failed = to, true
		}
		if acc.main {
			acc.net[e.idx] += e.netMs
		}
		if lv != nil {
			i := c.child.idx
			lv.att[i]++
			if failed {
				lv.fail[i]++
				lv.failMs[i] += d
			}
		}
		// Half-open: every sampled attempt measures the callee's health, but
		// only the permitted share is really sent; the rest fail fast.
		if pass < 1 && !s.bernoulli(pass) {
			return elapsed, true
		}
		elapsed += d
		if !failed {
			return elapsed, false
		}
		if lv == nil || e.retry == nil || k >= e.retry.Max || !s.bernoulli(lv.retryOK[c.child.idx]) {
			return elapsed, true
		}
		elapsed += backoffMs(e.retry, k+1)
	}
}

// sampleAsync samples each async edge as its own pipeline, splitting the
// sample budget across the call sites of the edge by their rate.
func (g *graph) sampleAsync(s *sampler, acc *mcAcc, reject []float64, budget int) []AsyncReport {
	type site struct {
		c    *call
		rate float64
	}
	sites := make([][]site, len(g.edges))
	for _, v := range g.visits {
		for _, grp := range v.groups {
			for _, c := range grp {
				if c.e.async {
					sites[c.e.idx] = append(sites[c.e.idx], site{c, v.rate * c.e.calls * c.e.prob})
				}
			}
		}
	}
	var out []AsyncReport
	for _, e := range g.edges {
		total := 0.0
		for _, st := range sites[e.idx] {
			total += st.rate
		}
		if total == 0 {
			continue
		}
		var durations []float64
		for _, st := range sites[e.idx] {
			for range int(math.Round(float64(budget) * st.rate / total)) {
				d, _ := g.sample(st.c.child, s, acc, reject)
				durations = append(durations, d+e.netMs)
			}
		}
		if len(durations) == 0 {
			continue
		}
		l := summarize(durations)
		out = append(out, AsyncReport{Edge: e.id, P50Ms: l.P50Ms, P99Ms: l.P99Ms})
	}
	return out
}

func (g *graph) hops(acc *mcAcc, samples float64) []HopReport {
	visits := make([]float64, len(g.nodes))
	for _, v := range g.visits {
		if !v.async {
			visits[v.node.idx] += g.weights[v.class] * v.perReq
		}
	}
	netIn := make([]float64, len(g.nodes))
	for _, e := range g.edges {
		netIn[e.to.idx] += acc.net[e.idx]
	}
	var out []HopReport
	for _, n := range g.nodes {
		if visits[n.idx] == 0 || n.arch == catalog.Client {
			continue
		}
		out = append(out, HopReport{
			Node:             n.m.ID,
			VisitsPerRequest: visits[n.idx],
			NetworkMs:        netIn[n.idx] / samples,
			WaitMs:           acc.wait[n.idx] / samples,
			ServiceMs:        acc.service[n.idx] / samples,
			DelayMs:          acc.delay[n.idx] / samples,
		})
	}
	return out
}

// summarize sorts durations in place and returns mean and nearest-rank percentiles.
func summarize(d []float64) LatencyStats {
	sort.Float64s(d)
	sum := 0.0
	for _, x := range d {
		sum += x
	}
	pct := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(d)))) - 1
		return d[min(max(i, 0), len(d)-1)]
	}
	return LatencyStats{MeanMs: sum / float64(len(d)), P50Ms: pct(0.50), P95Ms: pct(0.95), P99Ms: pct(0.99)}
}
