package sim

import (
	"math"
	"slices"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// Retries and circuit breakers (PLAN v1.1 #7). Only the time simulation
// models them: a failed attempt comes back as a retry after its failure
// latency plus backoff, in a later tick, so retry storms build up over time.
// The quick check treats a failed call as final.

// live is the per-tick state of the time simulation that the Monte Carlo
// pass reads. The quick check leaves graph.live nil: no faults, no breakers,
// no retries.
type live struct {
	down        []string  // per node: "" when up, else model.ModeError / ModeHang
	primaryDown []string  // per node: the datastore primary is failing over
	extraMs     []float64 // per node: injected latency per visit
	extraNetMs  []float64 // per edge: injected latency per call
	pass        []float64 // per edge: share of calls the circuit breaker lets through
	retryOK     []float64 // per visit: share of wanted retries the budget allows
	hangMs      float64   // how long a hung call lasts before the client gives up

	// Sampled this tick, per visit (the callee side of a call site).
	att, fail, failMs []float64
}

func newLive(nodes, edges, visits int, hangMs float64) *live {
	lv := &live{
		down: make([]string, nodes), primaryDown: make([]string, nodes), extraMs: make([]float64, nodes),
		extraNetMs: make([]float64, edges), pass: make([]float64, edges),
		retryOK: make([]float64, visits), att: make([]float64, visits), fail: make([]float64, visits), failMs: make([]float64, visits),
		hangMs: hangMs,
	}
	for i := range lv.pass {
		lv.pass[i] = 1
	}
	for i := range lv.retryOK {
		lv.retryOK[i] = 1
	}
	return lv
}

// backoffMs is the wait before retry k (1-based): exponential from BackoffMs.
func backoffMs(r *model.Retry, k int) float64 {
	return r.BackoffMs * math.Pow(2, float64(k-1))
}

// retrySite tracks the retries of one call site: the callee visit of an edge
// with a retry policy.
type retrySite struct {
	e      *gedge
	due    map[int][]float64 // tick → retries/s arriving, per generation (index k-1)
	gens   []float64         // attempts/s this tick per generation (0 = first attempts)
	capped bool              // the budget dropped retries this tick
}

func newRetrySite(e *gedge) *retrySite {
	return &retrySite{e: e, due: map[int][]float64{}, gens: make([]float64, e.retry.Max+1)}
}

// release returns the retries/s sent at tick i next to first new attempts,
// after the circuit breaker (pass) and the retry budget, and the share of
// wanted retries the budget allows.
func (rs *retrySite) release(i int, first, pass float64) (retries, ok float64) {
	due := rs.due[i]
	delete(rs.due, i)
	want := 0.0
	for _, r := range due {
		want += r * pass
	}
	ok = 1
	if b := rs.e.retry.BudgetPercent; b != nil && want > 0 {
		if allowed := *b / 100 * first; allowed < want {
			ok = allowed / want
		}
	}
	rs.gens[0] = first
	for k := 1; k < len(rs.gens); k++ {
		rs.gens[k] = 0
		if k-1 < len(due) {
			rs.gens[k] = due[k-1] * pass * ok
		}
	}
	rs.capped = ok < 1
	return want * ok, ok
}

// schedule turns this tick's failed attempts into the next generation of
// retries. A retry is sent failMs (time to detect the failure) plus backoff
// after the attempt; attempts are spread over the tick, so the retries split
// between the two ticks that delay straddles. Retries never land in the same
// tick: its flow is already computed.
func (rs *retrySite) schedule(i int, f, failMs float64) {
	for k := 0; k < len(rs.gens)-1; k++ {
		rate := rs.gens[k] * f
		if rate == 0 {
			continue
		}
		x := (failMs + backoffMs(rs.e.retry, k+1)) / 1000 / tickS
		whole := math.Floor(x)
		frac := x - whole
		rs.add(i+max(1, int(whole)), k, rate*(1-frac))
		rs.add(i+max(1, int(whole)+1), k, rate*frac)
	}
}

func (rs *retrySite) add(tick, gen int, rate float64) {
	if rate == 0 {
		return
	}
	d := rs.due[tick]
	if d == nil {
		d = make([]float64, len(rs.gens)-1)
		rs.due[tick] = d
	}
	d[gen] += rate
}

// breakerCfg is a circuit breaker policy with defaults filled in.
type breakerCfg struct {
	thresholdPct float64
	windowS      int
	minCalls     float64
	openS        float64
	halfOpen     float64
}

const r4jURL = "https://resilience4j.readme.io/docs/circuitbreaker"

func (g *graph) breakerConfig(e *gedge, cb *model.CircuitBreaker) *breakerCfg {
	pick := func(key string, v, def float64, src catalog.Source) float64 {
		if v > 0 {
			g.params.record(e.id, "circuitBreaker."+key, v, userSource)
			return v
		}
		g.params.record(e.id, "circuitBreaker."+key, def, src)
		return def
	}
	docs := func(note string) catalog.Source {
		return catalog.Source{Kind: catalog.SourceVendorDocs, URL: r4jURL, Note: note}
	}
	return &breakerCfg{
		thresholdPct: pick("failureRatePercent", cb.FailureRatePercent, 50, docs("Resilience4j failureRateThreshold default")),
		windowS: int(pick("windowS", float64(cb.WindowS), 10, catalog.Source{Kind: catalog.SourceDefaultEstimate,
			Note: "Resilience4j defaults to a count-based window of 100 calls; a 10 s time-based window is used here"})),
		minCalls: pick("minCalls", float64(cb.MinCalls), 100, docs("Resilience4j minimumNumberOfCalls default")),
		openS:    pick("openS", cb.OpenS, 60, docs("Resilience4j waitDurationInOpenState default")),
		halfOpen: pick("halfOpenCalls", float64(cb.HalfOpenCalls), 10, docs("Resilience4j permittedNumberOfCallsInHalfOpenState default")),
	}
}

const (
	breakerClosed   = "closed"
	breakerOpen     = "open"
	breakerHalfOpen = "half-open"
)

// breaker is the state machine of one edge's circuit breaker. Calls and
// failures are aggregated over all call sites of the edge.
type breaker struct {
	e            *gedge
	cfg          *breakerCfg
	state        string
	until        float64   // open: when probing starts
	calls, fails []float64 // closed: per-second ring over the window
	demand       float64   // calls/s wanted this tick, before the breaker
}

func newBreaker(e *gedge) *breaker {
	return &breaker{e: e, cfg: e.breaker, state: breakerClosed,
		calls: make([]float64, e.breaker.windowS), fails: make([]float64, e.breaker.windowS)}
}

// pass is the share of calls let through: all when closed, none when open,
// and halfOpen calls per second when probing.
func (b *breaker) pass() float64 {
	switch b.state {
	case breakerOpen:
		return 0
	case breakerHalfOpen:
		if b.demand > b.cfg.halfOpen {
			return b.cfg.halfOpen / b.demand
		}
	}
	return 1
}

func (b *breaker) open(t float64) {
	b.state, b.until = breakerOpen, t+b.cfg.openS
	clear(b.calls)
	clear(b.fails)
}

// dispatch sends the calls of node n's visits to their callees for tick i:
// first attempts the circuit breaker lets through, plus retries due now.
func (s *simState) dispatch(i int, n *gnode, visits []*visit) {
	for _, v := range visits {
		for _, grp := range v.groups {
			for _, c := range grp {
				if b := s.breakers[c.e.idx]; b != nil {
					b.demand += s.served[v.idx] * c.e.calls * c.e.prob
				}
			}
		}
	}
	for _, e := range n.out {
		if b := s.breakers[e.idx]; b != nil {
			s.lv.pass[e.idx] = b.pass()
			b.demand = 0
		}
	}
	callerUp := s.lv.down[n.idx] == ""
	for _, v := range visits {
		for _, grp := range v.groups {
			for _, c := range grp {
				pass := s.lv.pass[c.e.idx]
				first := s.served[v.idx] * c.e.calls * c.e.prob * pass
				load := first
				if rs := s.retries[c.child.idx]; rs != nil {
					if !callerUp {
						pass = 0 // a failed caller retries nothing
					}
					r, ok := rs.release(i, first, pass)
					load += r
					s.lv.retryOK[c.child.idx] = ok
				}
				s.firsts[c.child.idx], s.attempts[c.child.idx] = first, load
				s.offered[c.child.idx] += load
			}
		}
	}
}

// settleCalls records the failures sampled this tick, schedules the retries
// they cause and advances the circuit breakers.
func (s *simState) settleCalls(i int, t float64) {
	lv := s.lv
	for _, v := range s.g.visits {
		if lv.att[v.idx] > 0 {
			s.failRate[v.idx] = lv.fail[v.idx] / lv.att[v.idx]
			if lv.fail[v.idx] > 0 {
				s.failMs[v.idx] = lv.failMs[v.idx] / lv.fail[v.idx]
			}
		}
		if rs := s.retries[v.idx]; rs != nil {
			rs.schedule(i, s.failRate[v.idx], s.failMs[v.idx])
		}
	}
	for _, e := range s.resEdges {
		if e.retry != nil && e.retry.BudgetPercent != nil {
			capped := slices.ContainsFunc(s.sites[e.idx], func(v *visit) bool { return s.retries[v.idx].capped })
			switch {
			case capped && !s.retryCapped[e.idx]:
				s.event(t, e.id, "retry budget exhausted (%.0f%% of first attempts): further retries are dropped", *e.retry.BudgetPercent)
			case !capped && s.retryCapped[e.idx]:
				s.event(t, e.id, "retries back within budget")
			}
			s.retryCapped[e.idx] = capped
		}
		if b := s.breakers[e.idx]; b != nil {
			s.settleBreaker(b, i, t)
		}
	}
}

// probeBreakers moves breakers whose open time is over to half-open.
func (s *simState) probeBreakers(t float64) {
	for _, b := range s.breakers {
		if b != nil && b.state == breakerOpen && t >= b.until {
			b.state = breakerHalfOpen
			s.event(t, b.e.id, "circuit breaker half-open: %.0f probe call(s)/s", b.cfg.halfOpen)
		}
	}
}

func (s *simState) settleBreaker(b *breaker, i int, t float64) {
	switch b.state {
	case breakerClosed:
		calls, fails := 0.0, 0.0
		for _, v := range s.sites[b.e.idx] {
			calls += s.attempts[v.idx] * tickS
			fails += s.attempts[v.idx] * tickS * s.failRate[v.idx]
		}
		k := i % len(b.calls)
		b.calls[k], b.fails[k] = calls, fails
		total, failed := 0.0, 0.0
		for k := range b.calls {
			total += b.calls[k]
			failed += b.fails[k]
		}
		if total > 0 && total >= b.cfg.minCalls && failed/total*100 >= b.cfg.thresholdPct {
			b.open(t)
			s.event(t, b.e.id, "circuit breaker opens: %.0f%% of %.0f calls failed in the last %ds; calls fail fast for %.0fs",
				failed/total*100, total, len(b.calls), b.cfg.openS)
		}
	case breakerHalfOpen:
		rate, probes := b.probeRate(s)
		switch {
		case probes == 0:
		case rate*100 < b.cfg.thresholdPct:
			b.state = breakerClosed
			s.event(t, b.e.id, "circuit breaker closes: %.0f%% of probes failed", rate*100)
		default:
			b.open(t)
			s.event(t, b.e.id, "circuit breaker re-opens: %.0f%% of probes failed", rate*100)
		}
	}
}

// probeRate is the failed share of the attempts sampled over the edge this
// tick; while half-open every sampled attempt is a probe of the callee.
func (b *breaker) probeRate(s *simState) (rate, sampled float64) {
	failed := 0.0
	for _, v := range s.sites[b.e.idx] {
		sampled += s.lv.att[v.idx]
		failed += s.lv.fail[v.idx]
	}
	if sampled == 0 {
		return 0, 0
	}
	return failed / sampled, sampled
}
