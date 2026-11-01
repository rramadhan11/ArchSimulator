package sim

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// Time simulation (PLAN v1.1 #1 and #12). The compiled stations are stepped
// once per second:
//
//   - Flow: offered visits propagate down the call trees in topological
//     order. A station serves at most its capacity per tick; the excess stays
//     as a fluid backlog that drains FIFO, shared in proportion to work.
//     Backlog older than the queue timeout is dropped (counted as errors).
//   - Latency: each station is a pointwise-stationary M/M/c queue at the
//     rate it actually serves (same formulas as the quick check, so both
//     modes agree in steady state). Saturated stations instead add the
//     deterministic FIFO wait behind their backlog. Monte Carlo then samples
//     requests, so edge timeouts turn long waits into errors.
//   - Control: HPA / VM autoscalers act on the CPU actually served (a
//     saturated pod cannot report more than its limit), with sync period,
//     tolerance, scale-up policy and scale-down stabilization. New replicas
//     become ready after a startup delay; pods that do not fit stay Pending
//     and make the cluster autoscaler provision nodes after a delay; it
//     removes nodes that stay unneeded.
//   - Resilience: failed attempts return as retries in later ticks (with
//     backoff and an optional budget) and circuit breakers fail calls fast
//     while open (resilience.go). Injected failures take out instances,
//     zones or dependencies, add latency, or fail a database over (faults.go).
//
// Not modeled: memory pressure (OOM kills, eviction), and the threads or
// connections a caller holds while waiting on a hung dependency.

const tickS = 1.0

// Timeline is the result of a time simulation. Per-tick series are parallel
// to Services, Stations and Pools.
type Timeline struct {
	CatalogVersion string   `json:"catalogVersion"`
	TickS          float64  `json:"tickS"`
	Services       []string `json:"services"`
	Stations       []string `json:"stations"`
	Pools          []string `json:"pools,omitempty"`
	// Edges lists the edges with a retry policy or circuit breaker.
	Edges       []string        `json:"edges,omitempty"`
	Failures    []FailureSpan   `json:"failures,omitempty"`
	Ticks       []Tick          `json:"ticks"`
	Events      []Event         `json:"events"`
	Summary     TimelineSummary `json:"summary"`
	Issues      []Issue         `json:"issues,omitempty"`
	Assumptions []Assumption    `json:"assumptions"`
}

type Tick struct {
	T          float64 `json:"t"`
	OfferedTPS float64 `json:"offeredTps"`
	ErrorRate  float64 `json:"errorRate"`
	// Latency is nil when every sampled request failed.
	Latency  *LatencyStats `json:"latency,omitempty"`
	Replicas []ReplicaTick `json:"replicas"`
	Stations []StationTick `json:"stations"`
	Pools    []PoolTick    `json:"pools,omitempty"`
	Edges    []EdgeTick    `json:"edges,omitempty"`
}

type EdgeTick struct {
	Attempts float64 `json:"attempts"` // calls/s sent, retries included
	Retries  float64 `json:"retries"`  // retries/s among them
	// FailRate is the sampled share of attempts that failed (errors and
	// timeouts); while half-open, the share of probes.
	FailRate float64 `json:"failRate"`
	Breaker  string  `json:"breaker,omitempty"`
}

type ReplicaTick struct {
	Desired int `json:"desired"`
	Ready   int `json:"ready"`
}

type StationTick struct {
	// Load is newly offered work over capacity; above 1 the backlog grows.
	// Nil when the station has no capacity.
	Load    *float64 `json:"load"`
	Backlog float64  `json:"backlog"` // queued visits at the end of the tick
	WaitMs  float64  `json:"waitMs"`  // FIFO wait behind the backlog
}

type PoolTick struct {
	Ready        int `json:"ready"`
	Provisioning int `json:"provisioning"`
}

type Event struct {
	T       float64 `json:"t"`
	Ref     string  `json:"ref"`
	Message string  `json:"message"`
}

type TimelineSummary struct {
	PeakP99Ms    *float64 `json:"peakP99Ms"`
	MaxErrorRate float64  `json:"maxErrorRate"`
	// SLOBreachS counts seconds where p99 or the error rate broke the SLO.
	SLOBreachS   float64  `json:"sloBreachS"`
	FirstBreachS *float64 `json:"firstBreachS"`
}

// Simulate runs the time simulation configured in workload.timeline.
func Simulate(a *model.Architecture, cat *catalog.Catalog) (*Timeline, error) {
	g, err := compile(a, cat)
	if err != nil {
		return nil, err
	}
	g.checkCatalogVersion()
	w := a.Workload
	s := g.newSimState(w)
	if g.err != nil {
		return nil, g.err
	}
	for i := range w.Timeline.Duration() {
		s.step(i, w.TPSAt(float64(i)*tickS))
	}
	s.summarize()
	s.out.Issues = g.issues
	s.out.Assumptions = g.params.list()
	return s.out, nil
}

type simState struct {
	g        *graph
	w        model.Workload
	order    []*gnode   // topological order of the call graph
	byNode   [][]*visit // visits per node
	stations []*station // stations that receive visits
	stVisits [][]*visit // visits per entry of stations
	hosts    []*hostState
	pools    []*poolState

	backlog []float64 // per visit, in visits
	offered []float64 // per visit, visits/s this tick
	served  []float64 // per visit, visits/s this tick
	reject  []float64 // per node, probability a visit is refused this tick
	wasBusy []bool    // per output station, backlog at the previous tick

	lv          *live
	attempts    []float64    // per visit: calls/s sent to it this tick, retries included
	firsts      []float64    // per visit: of which first attempts
	failRate    []float64    // per visit: failed share of attempts, last sampled
	failMs      []float64    // per visit: mean duration of a failed attempt, last sampled
	sites       [][]*visit   // per edge: the callee visits it reaches
	retries     []*retrySite // per visit; nil without a retry policy
	breakers    []*breaker   // per edge; nil without a circuit breaker
	resEdges    []*gedge     // edges reported per tick
	retryCapped []bool       // per edge: the retry budget dropped retries last tick
	faults      []*fault

	timeoutS float64
	samples  int
	seed     uint64
	out      *Timeline
}

// scaler is an autoscaler control loop (HPA or VM target tracking).
type scaler struct {
	syncS       int
	tolerance   float64
	downWindowS float64
	upPercent   float64 // scale-up limit per sync; 0 with upPods 0 = unlimited
	upPods      int
}

type recommendation struct {
	t        float64
	replicas int
}

type hostState struct {
	h        *hosting
	ref      string // node the events refer to
	sc       *scaler
	startupS float64

	desired  int
	ready    int
	starting []float64 // ready-at times of scheduled replicas still starting
	usage    float64   // served cores summed since the last sync
	usageN   int
	recs     []recommendation
	pending  int
	pinned   bool
	down     int // unmanaged instances lost to a failure, not restarted
}

type poolState struct {
	p            *nodePool
	provisionS   float64
	unneededS    float64
	afterAddS    float64
	ready        int
	provisioning []float64 // ready-at times
	lastAdd      float64   // last cluster-autoscaler scale-up
	unneeded     []float64 // since when each unneeded node is unneeded, oldest first
}

func (g *graph) newSimState(w model.Workload) *simState {
	tl := w.Timeline
	s := &simState{
		g: g, w: w,
		byNode:   make([][]*visit, len(g.nodes)),
		backlog:  make([]float64, len(g.visits)),
		offered:  make([]float64, len(g.visits)),
		served:   make([]float64, len(g.visits)),
		reject:   make([]float64, len(g.nodes)),
		timeoutS: tl.QueueTimeout() / 1000,
		samples:  tl.TickSamples(),
		seed:     w.RandSeed(),
		out:      &Timeline{CatalogVersion: g.cat.Version, TickS: tickS},
	}
	s.order = g.topoOrder()
	byStation := map[*station][]*visit{}
	for _, v := range g.visits {
		s.byNode[v.node.idx] = append(s.byNode[v.node.idx], v)
		if v.st != nil {
			byStation[v.st] = append(byStation[v.st], v)
		}
	}
	for _, st := range g.stations {
		if vs := byStation[st]; vs != nil {
			s.stations = append(s.stations, st)
			s.stVisits = append(s.stVisits, vs)
			s.out.Stations = append(s.out.Stations, st.id)
		}
	}
	s.wasBusy = make([]bool, len(s.stations))

	// Start from the steady state at the initial load: the system has been
	// running at that rate before the simulated window.
	g.evaluate(w.TPSAt(0), s.samples, s.seed)

	for _, h := range g.hosts {
		hs := &hostState{h: h, ref: h.svc.m.ID, desired: h.desired, ready: h.scheduled}
		if h.host != nil {
			hs.ref = h.host.m.ID
		}
		switch {
		case h.host == nil:
		case h.host.arch == catalog.Orchestrator:
			hs.startupS = g.nonNeg(h.host, "podStartupS")
			if h.auto != nil {
				hs.sc = &scaler{
					syncS:       g.count(h.host, "hpa.syncPeriodS", 1),
					tolerance:   g.nonNeg(h.host, "hpa.tolerance"),
					downWindowS: g.nonNeg(h.host, "hpa.scaleDownStabilizationS"),
					upPercent:   g.nonNeg(h.host, "hpa.scaleUpPercent"),
					upPods:      g.count(h.host, "hpa.scaleUpPods", 0),
				}
			}
		case h.host.arch == catalog.RuntimeHost:
			if h.auto != nil {
				hs.startupS = g.nonNeg(h.host, "autoscaling.bootS")
				hs.sc = &scaler{
					syncS:       g.count(h.host, "autoscaling.evaluationS", 1),
					downWindowS: g.nonNeg(h.host, "autoscaling.scaleInCooldownS"),
				}
			}
		}
		if hs.sc != nil {
			hs.recs = []recommendation{{0, hs.desired}}
		}
		s.hosts = append(s.hosts, hs)
		s.out.Services = append(s.out.Services, h.svc.m.ID)
	}
	for _, p := range g.pools {
		ps := &poolState{p: p, ready: p.nodesNeeded, provisionS: g.nonNeg(p.n, "provisionS"),
			lastAdd: math.Inf(-1)}
		if p.autoscaled {
			ps.unneededS = g.nonNeg(p.n, "scaleDownUnneededS")
			ps.afterAddS = g.nonNeg(p.n, "scaleDownDelayAfterAddS")
		}
		s.pools = append(s.pools, ps)
		s.out.Pools = append(s.out.Pools, p.n.m.ID)
	}

	// Resilience state. It is attached after the initial steady state, which
	// (like the quick check) runs without faults or retries.
	s.lv = newLive(len(g.nodes), len(g.edges), len(g.visits), tl.QueueTimeout())
	g.live = s.lv
	n := len(g.visits)
	s.attempts, s.firsts, s.failRate, s.failMs = make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	s.sites = make([][]*visit, len(g.edges))
	s.retries = make([]*retrySite, n)
	s.breakers = make([]*breaker, len(g.edges))
	s.retryCapped = make([]bool, len(g.edges))
	for _, v := range g.visits {
		for _, grp := range v.groups {
			for _, c := range grp {
				s.sites[c.e.idx] = append(s.sites[c.e.idx], c.child)
				if c.e.retry != nil && !c.e.async {
					s.retries[c.child.idx] = newRetrySite(c.e)
				}
			}
		}
	}
	for _, e := range g.edges {
		if e.async || (e.retry == nil && e.breaker == nil) {
			continue
		}
		if e.breaker != nil {
			s.breakers[e.idx] = newBreaker(e)
		}
		if e.retry != nil && e.retry.BudgetPercent == nil {
			g.issues = append(g.issues, Issue{Severity: SeverityInfo, Ref: e.id,
				Message: fmt.Sprintf("no retry budget: while %s fails, retries can multiply its load up to %d×", e.to.m.ID, e.retry.Max+1)})
		}
		s.resEdges = append(s.resEdges, e)
		s.out.Edges = append(s.out.Edges, e.id)
	}
	s.resolveFaults()
	return s
}

// topoOrder returns nodes so that every caller precedes its callees (Kahn).
// Compile rejects reachable cycles, so only unreached nodes can be left out.
func (g *graph) topoOrder() []*gnode {
	indeg := make([]int, len(g.nodes))
	for _, e := range g.edges {
		indeg[e.to.idx]++
	}
	var queue, order []*gnode
	for _, n := range g.nodes {
		if indeg[n.idx] == 0 {
			queue = append(queue, n)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		for _, e := range n.out {
			if indeg[e.to.idx]--; indeg[e.to.idx] == 0 {
				queue = append(queue, e.to)
			}
		}
	}
	return order
}

func (s *simState) event(t float64, ref, format string, args ...any) {
	s.out.Events = append(s.out.Events, Event{T: t, Ref: ref, Message: fmt.Sprintf(format, args...)})
}

func (s *simState) step(i int, tps float64) {
	t := float64(i) * tickS
	s.applyFaults(t)
	s.provision(t)
	s.schedule(t)
	s.probeBreakers(t)
	s.flow(i, tps)

	for _, hs := range s.hosts {
		hs.usage += hs.h.svc.st.work / 1000 / hs.h.uslFactor
		hs.usageN++
	}
	tick := Tick{T: t, OfferedTPS: tps}
	clear(s.lv.att)
	clear(s.lv.fail)
	clear(s.lv.failMs)
	mc := s.g.monteCarlo(s.samples, s.seed+uint64(i), s.reject, false)
	tick.ErrorRate = mc.errorRate
	if mc.errorRate < 1 {
		tick.Latency = &mc.latency
	}
	s.settleCalls(i, t)
	if i > 0 {
		s.autoscale(i, t)
	}
	s.record(&tick)
	s.out.Ticks = append(s.out.Ticks, tick)
}

// provision moves replicas and nodes whose startup delay has elapsed to ready.
func (s *simState) provision(t float64) {
	for _, hs := range s.hosts {
		n := 0
		for n < len(hs.starting) && hs.starting[n] <= t {
			n++
		}
		hs.ready += n
		hs.starting = hs.starting[n:]
	}
	for _, ps := range s.pools {
		n := 0
		for n < len(ps.provisioning) && ps.provisioning[n] <= t {
			n++
		}
		if n > 0 {
			ps.ready += n
			ps.provisioning = ps.provisioning[n:]
			s.event(t, ps.p.n.m.ID, "%d node(s) ready (%d total)", n, ps.ready)
		}
	}
}

// schedule places desired replicas on the available nodes, starts new ones,
// stops surplus ones, and requests nodes for pods that stay Pending.
func (s *simState) schedule(t float64) {
	for _, hs := range s.hosts {
		hs.h.desired = hs.desired
		hs.h.scheduled = hs.desired
	}
	used := make([]int, len(s.pools))
	for k, ps := range s.pools {
		used[k] = ps.p.pack(ps.ready)
	}
	for _, hs := range s.hosts {
		running := hs.ready + len(hs.starting)
		switch target := hs.h.scheduled - hs.down; {
		case target > running:
			for range target - running {
				hs.starting = append(hs.starting, t+hs.startupS)
			}
		case target < running:
			drop := running - target
			cut := min(drop, len(hs.starting))
			hs.starting = hs.starting[:len(hs.starting)-cut] // newest first
			hs.ready -= drop - cut
		}
		pending := hs.desired - hs.h.scheduled
		if pending > 0 && hs.pending == 0 {
			s.event(t, hs.ref, "%d pod(s) Pending: no room on the node pool", pending)
		}
		hs.pending = pending
	}
	for k, ps := range s.pools {
		if !ps.p.autoscaled {
			continue
		}
		if !slices.ContainsFunc(s.hosts, func(hs *hostState) bool { return hs.h.pool == ps.p && hs.pending > 0 }) {
			s.scaleDownPool(ps, used[k], t)
			continue
		}
		ps.unneeded = ps.unneeded[:0]
		need := ps.p.pack(ps.p.maxNodes)
		ps.p.pack(ps.ready) // restore the actual placement
		if add := need - ps.ready - len(ps.provisioning); add > 0 {
			for range add {
				ps.provisioning = append(ps.provisioning, t+ps.provisionS)
			}
			ps.lastAdd = t
			s.event(t, ps.p.n.m.ID, "cluster autoscaler requests %d node(s), ready in %.0fs", add, ps.provisionS)
		}
	}
}

// scaleDownPool removes each node the pods no longer need once it has been
// unneeded for scaleDownUnneededS (and not within scaleDownDelayAfterAddS of
// a scale-up). Pods are packed first-fit decreasing, so the surplus nodes are
// empty: the model does not evict and restart pods the way draining an
// underutilized node does, nor apply the 50% utilization threshold.
func (s *simState) scaleDownPool(ps *poolState, used int, t float64) {
	surplus := ps.ready - used
	if len(ps.provisioning) > 0 {
		surplus = 0
	}
	if surplus < len(ps.unneeded) {
		ps.unneeded = ps.unneeded[:max(surplus, 0)] // the most recently unneeded are needed again
	}
	for len(ps.unneeded) < surplus {
		ps.unneeded = append(ps.unneeded, t)
	}
	if t-ps.lastAdd < ps.afterAddS {
		return
	}
	remove := 0
	for remove < len(ps.unneeded) && t-ps.unneeded[remove] >= ps.unneededS {
		remove++
	}
	if remove > 0 {
		ps.ready -= remove
		ps.unneeded = ps.unneeded[remove:]
		s.event(t, ps.p.n.m.ID, "cluster autoscaler removes %d unneeded node(s) (%d left)", remove, ps.ready)
	}
}

// flow propagates offered load through the stations for one tick.
func (s *simState) flow(i int, tps float64) {
	g := s.g
	clear(s.offered)
	clear(s.reject)
	for c, root := range g.trees {
		s.offered[root.idx] = tps * g.weights[c]
	}
	for _, hs := range s.hosts {
		active := hs.ready
		if hs.h.partitionCap > 0 {
			active = min(active, hs.h.partitionCap)
		}
		hs.h.svc.st.servers = active * hs.h.k
	}
	for _, st := range g.stations {
		st.lambda, st.work, st.s2 = 0, 0, 0
		st.q, st.meanS, st.fluidMs, st.fluidLoMs, st.offeredLoad, st.down = QueueStats{}, 0, 0, 0, 0, false
	}

	for _, n := range s.order {
		visits := s.byNode[n.idx]
		if len(visits) == 0 {
			continue
		}
		// A datastore node can feed two stations (primary, replicas); keep
		// first-seen order so results stay deterministic.
		var sts []*station
		byStation := map[*station][]*visit{}
		for _, v := range visits {
			// A failed node (or failing-over primary) serves nothing; what it
			// had queued is lost.
			if s.lv.down[n.idx] != "" || (s.lv.primaryDown[n.idx] != "" && v.st != nil && v.st == n.st) {
				s.served[v.idx], s.backlog[v.idx] = 0, 0
				if v.st != nil {
					v.st.down = true
				}
				continue
			}
			if v.st == nil || v.serviceMs == 0 {
				s.served[v.idx] = s.offered[v.idx]
				continue
			}
			if byStation[v.st] == nil {
				sts = append(sts, v.st)
			}
			byStation[v.st] = append(byStation[v.st], v)
		}
		for _, st := range sts {
			s.serveStation(st, byStation[st])
		}
		if n.rateLimit > 0 {
			total := 0.0
			for _, v := range visits {
				total += s.served[v.idx]
			}
			if total > n.rateLimit {
				keep := n.rateLimit / total
				s.reject[n.idx] = 1 - (1-s.reject[n.idx])*keep
				for _, v := range visits {
					s.served[v.idx] *= keep
				}
			}
		}
		s.dispatch(i, n, visits)
	}

	for _, st := range s.stations {
		if st.lambda == 0 {
			continue
		}
		mean := st.work / st.lambda
		st.meanS = mean / st.speed
		if st.fluidMs > 0 || st.fluidLoMs > 0 {
			st.q = QueueStats{Rho: 1} // the fluid backlog stands in for the stochastic wait
			continue
		}
		cs2 := st.s2/(st.lambda*mean*mean) - 1
		if q := Queue(st.lambda, st.meanS, cs2, st.servers); q.Stable() {
			st.q = q
		} else {
			st.q = QueueStats{Rho: 1}
		}
	}
}

// serveStation serves the visits of one station for a tick: new arrivals
// plus backlog, up to capacity, shared in proportion to their work.
func (s *simState) serveStation(st *station, vs []*visit) {
	capWork := float64(st.servers) * st.speed * 1000 * tickS // ms of speed-1 work per tick
	demand, offeredWork, carried := 0.0, 0.0, 0.0
	for _, v := range vs {
		demand += (s.offered[v.idx]*tickS + s.backlog[v.idx]) * v.serviceMs
		offeredWork += s.offered[v.idx] * tickS * v.serviceMs
		carried += s.backlog[v.idx] * v.serviceMs
	}
	frac := 1.0
	if demand > capWork {
		frac = capWork / demand
	}
	left, arrivals, dropped := 0.0, 0.0, 0.0
	for _, v := range vs {
		d := s.offered[v.idx]*tickS + s.backlog[v.idx]
		arrivals += s.offered[v.idx] * tickS
		s.served[v.idx] = frac * d / tickS
		s.backlog[v.idx] = (1 - frac) * d
		left += s.backlog[v.idx] * v.serviceMs
	}
	waitS := math.Inf(1)
	if capWork > 0 {
		waitS = left / capWork * tickS
	}
	if left > 0 && waitS > s.timeoutS {
		keep := 0.0
		if capWork > 0 {
			keep = s.timeoutS / waitS
		}
		for _, v := range vs {
			dropped += s.backlog[v.idx] * (1 - keep)
			s.backlog[v.idx] *= keep
		}
		waitS = s.timeoutS
		if capWork == 0 {
			waitS = 0
		}
	}

	// In a sustained overload the drops per tick equal arrivals minus
	// capacity, so dropped/arrivals is the share of new requests that fail.
	n := st.node
	if dropped > 0 && arrivals > 0 {
		s.reject[n.idx] = 1 - (1-s.reject[n.idx])*(1-math.Min(1, dropped/arrivals))
	}
	if capWork == 0 && arrivals > 0 {
		s.reject[n.idx] = 1
	}
	for _, v := range vs {
		r := s.served[v.idx]
		st.lambda += r
		st.work += r * v.serviceMs
		st.s2 += r * v.serviceMs * v.serviceMs * (1 + v.cs2)
	}
	if capWork > 0 {
		if left > 0 {
			st.fluidMs = waitS * 1000
		}
		st.fluidLoMs = math.Min(carried/capWork*tickS, s.timeoutS) * 1000
	}
	st.offeredLoad = offeredWork / capWork
}

// autoscale runs the control loops whose sync period ends at tick i.
func (s *simState) autoscale(i int, t float64) {
	for _, hs := range s.hosts {
		sc, a := hs.sc, hs.h.auto
		if sc == nil || i%sc.syncS != 0 {
			continue
		}
		cores := hs.usage / float64(hs.usageN)
		hs.usage, hs.usageN = 0, 0
		if hs.ready == 0 {
			continue // no metrics yet
		}
		ratio := cores / (float64(hs.ready) * a.basis * a.target)
		want := int(math.Ceil(cores/(a.basis*a.target) - 1e-9))
		rec := hs.desired
		if math.Abs(ratio-1) > sc.tolerance {
			rec = want
		}
		rec = min(max(rec, a.min), a.max)

		hs.recs = append(hs.recs, recommendation{t, rec})
		keep := 0
		for keep < len(hs.recs) && hs.recs[keep].t < t-sc.downWindowS {
			keep++
		}
		hs.recs = hs.recs[keep:]

		next := rec
		switch {
		case rec > hs.desired && (sc.upPercent > 0 || sc.upPods > 0):
			limit := max(int(math.Ceil(float64(hs.desired)*(1+sc.upPercent/100))), hs.desired+sc.upPods)
			next = min(rec, limit)
		case rec < hs.desired:
			for _, r := range hs.recs {
				next = max(next, r.replicas)
			}
			next = min(next, hs.desired)
		}
		label := strings.ToUpper(a.kind)
		if next != hs.desired {
			s.event(t, hs.ref, "%s scales %d → %d replicas (CPU at %.0f%% of target)", label, hs.desired, next, ratio*100)
			hs.desired = next
		}
		pinned := hs.desired == a.max && want > a.max
		if pinned && !hs.pinned {
			s.event(t, hs.ref, "%s pinned at max %d replicas (needs %d)", label, a.max, want)
		}
		hs.pinned = pinned
	}
}

func (s *simState) record(tick *Tick) {
	for _, hs := range s.hosts {
		tick.Replicas = append(tick.Replicas, ReplicaTick{Desired: hs.desired, Ready: hs.ready})
	}
	for k, st := range s.stations {
		backlog := 0.0
		for _, v := range s.stVisits[k] {
			backlog += s.backlog[v.idx]
		}
		var load *float64
		if !st.down && !math.IsInf(st.offeredLoad, 0) && !math.IsNaN(st.offeredLoad) {
			load = ptr(st.offeredLoad)
		}
		tick.Stations = append(tick.Stations, StationTick{Load: load, Backlog: backlog, WaitMs: st.fluidMs})
		busy := backlog > 1e-9
		switch {
		case busy && !s.wasBusy[k]:
			s.event(tick.T, st.id, "backlog building: demand exceeds capacity")
		case !busy && s.wasBusy[k]:
			s.event(tick.T, st.id, "backlog drained")
		}
		s.wasBusy[k] = busy
	}
	for _, ps := range s.pools {
		tick.Pools = append(tick.Pools, PoolTick{Ready: ps.ready, Provisioning: len(ps.provisioning)})
	}
	for _, e := range s.resEdges {
		var et EdgeTick
		failed := 0.0
		for _, v := range s.sites[e.idx] {
			et.Attempts += s.attempts[v.idx]
			et.Retries += s.attempts[v.idx] - s.firsts[v.idx]
			failed += s.attempts[v.idx] * s.failRate[v.idx]
		}
		if et.Attempts > 0 {
			et.FailRate = failed / et.Attempts
		}
		if b := s.breakers[e.idx]; b != nil {
			et.Breaker = b.state
			if b.state == breakerHalfOpen {
				et.FailRate, _ = b.probeRate(s)
			}
		}
		tick.Edges = append(tick.Edges, et)
	}
}

func (s *simState) summarize() {
	sum := &s.out.Summary
	slo := s.w.SLO
	budget := s.w.ErrorBudget()
	for _, tk := range s.out.Ticks {
		sum.MaxErrorRate = math.Max(sum.MaxErrorRate, tk.ErrorRate)
		breach := tk.ErrorRate > budget
		if l := tk.Latency; l != nil {
			if sum.PeakP99Ms == nil || l.P99Ms > *sum.PeakP99Ms {
				sum.PeakP99Ms = ptr(l.P99Ms)
			}
			breach = breach || (slo != nil && slo.P99Ms > 0 && l.P99Ms > slo.P99Ms)
		}
		if breach {
			sum.SLOBreachS += s.out.TickS
			if sum.FirstBreachS == nil {
				sum.FirstBreachS = ptr(tk.T)
			}
		}
	}
}
