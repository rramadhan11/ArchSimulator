package sim

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"archsim/engine/catalog"
)

// evaluate computes the steady state at one TPS value. It mutates the
// per-evaluation fields of the graph, so a graph must not be evaluated
// concurrently.
func (g *graph) evaluate(tps float64, samples int, seed uint64) *Report {
	r := &Report{TPS: tps, Stable: true}
	r.Issues = append(r.Issues, g.issues...)

	for _, st := range g.stations {
		st.lambda, st.work, st.s2 = 0, 0, 0
	}
	nodeRate := make([]float64, len(g.nodes))
	edgeRate := make([]float64, len(g.edges))
	for _, v := range g.visits {
		v.rate = tps * g.weights[v.class] * v.perReq
		nodeRate[v.node.idx] += v.rate
		if st := v.st; st != nil && v.rate > 0 {
			st.lambda += v.rate
			st.work += v.rate * v.serviceMs
			st.s2 += v.rate * v.serviceMs * v.serviceMs * (1 + v.cs2) // E[S²] of a mixture
		}
		for _, grp := range v.groups {
			for _, c := range grp {
				edgeRate[c.e.idx] += v.rate * c.e.calls * c.e.prob
			}
		}
	}

	g.resolveReplicas(r)

	for _, st := range g.stations {
		st.q, st.meanS, st.fluidMs = QueueStats{}, 0, 0
		if st.lambda == 0 {
			continue
		}
		mean := st.work / st.lambda
		cs2 := 0.0
		if mean > 0 {
			cs2 = st.s2/(st.lambda*mean*mean) - 1
		}
		st.meanS = mean / st.speed
		st.q = Queue(st.lambda, st.meanS, cs2, st.servers)
		if !st.q.Stable() {
			r.Stable = false
		}
	}

	reject := make([]float64, len(g.nodes))
	for _, n := range g.nodes {
		if n.rateLimit > 0 && nodeRate[n.idx] > n.rateLimit {
			reject[n.idx] = 1 - n.rateLimit/nodeRate[n.idx]
		}
	}

	var mc *mcResult
	if r.Stable {
		mc = g.monteCarlo(samples, seed, reject, true)
		r.Latency, r.ErrorRate = &mc.latency, &mc.errorRate
		r.Hops, r.Async = mc.hops, mc.async
	}
	r.Resources = g.resources(nodeRate, edgeRate, mc)
	r.Assumptions = g.params.list()
	g.judge(r)
	r.Confidence = g.confidence(r)
	return r
}

func (g *graph) resolveReplicas(r *Report) {
	for _, h := range g.hosts {
		h.desired, h.scaleCapped = h.fixed, false
		if a := h.auto; a != nil {
			// Steady state of HPA / target tracking: the replica count at which
			// average utilization (relative to the basis) equals the target.
			demandCores := h.svc.st.work / 1000 / h.uslFactor
			need := int(math.Ceil(demandCores/(a.basis*a.target) - 1e-9))
			h.desired = min(max(need, a.min), a.max)
			h.scaleCapped = need > a.max
		}
		h.scheduled, h.pending = h.desired, 0
	}

	for _, p := range g.pools {
		p.nodesNeeded = p.pack(p.capacityNodes())
		pending := 0
		for _, h := range p.deployments {
			pending += h.pending
			if h.pending > 0 {
				r.Issues = append(r.Issues, Issue{Severity: SeverityError, Ref: h.host.m.ID,
					Message: fmt.Sprintf("%d of %d pods stay Pending: node pool %q has no room (requests %.2f vCPU / %.0f MiB per pod)",
						h.pending, h.desired, p.n.m.ID, h.reqCPU, h.reqMem/(1<<20))})
			}
		}
		r.NodePools = append(r.NodePools, PoolReport{
			Node: p.n.m.ID, NodesNeeded: p.nodesNeeded, NodesAvailable: p.capacityNodes(), PendingPods: pending,
			AllocatableCPU: p.allocCPU, AllocatableMemGiB: p.allocMem / (1 << 30),
		})
	}

	for _, h := range g.hosts {
		active := h.scheduled
		rep := ReplicaReport{Service: h.svc.m.ID, Desired: h.desired, Scheduled: h.scheduled, Pending: h.pending}
		if h.host != nil {
			rep.Host = h.host.m.ID
		}
		if a := h.auto; a != nil {
			rep.Autoscaler, rep.Min, rep.Max = a.kind, a.min, a.max
			if h.scaleCapped {
				r.Issues = append(r.Issues, Issue{Severity: SeverityWarning, Ref: h.host.m.ID,
					Message: fmt.Sprintf("autoscaler is pinned at max (%d replicas); raising max would add capacity", a.max)})
			}
		}
		if h.partitionCap > 0 && active > h.partitionCap {
			active = h.partitionCap
			rep.Consuming = active
			r.Issues = append(r.Issues, Issue{Severity: SeverityWarning, Ref: h.svc.m.ID,
				Message: fmt.Sprintf("%d replicas but only %d partitions: %d consumers stay idle", h.scheduled, h.partitionCap, h.scheduled-h.partitionCap)})
		}
		h.svc.st.servers = active * h.k
		r.Replicas = append(r.Replicas, rep)
	}

	configured := make([]int, len(g.nodes))
	for _, p := range g.connPools {
		configured[p.to.idx] += p.from.host.scheduled * p.size
	}
	for _, n := range g.nodes {
		if n.arch == catalog.Datastore && configured[n.idx] > n.maxConns {
			r.Issues = append(r.Issues, Issue{Severity: SeverityWarning, Ref: n.m.ID,
				Message: fmt.Sprintf("connection pools of all replicas can open %d connections, above maxConnections %d; pools that pre-open connections will be refused", configured[n.idx], n.maxConns)})
		}
	}
}

// pack schedules the desired pods of every deployment onto the given number
// of nodes with first-fit decreasing, setting scheduled/pending per deployment.
// It returns the nodes needed: those used, but at least the pool minimum.
func (p *nodePool) pack(nodes int) int {
	freeCPU := make([]float64, nodes)
	freeMem := make([]float64, nodes)
	used := make([]bool, nodes)
	for i := range freeCPU {
		freeCPU[i], freeMem[i] = p.allocCPU, p.allocMem
	}
	order := slices.Clone(p.deployments)
	sort.SliceStable(order, func(i, j int) bool { return order[i].reqCPU > order[j].reqCPU })
	for _, h := range order {
		h.scheduled = 0
		for range h.desired {
			placed := false
			for j := range freeCPU {
				if freeCPU[j] >= h.reqCPU-1e-9 && freeMem[j] >= h.reqMem {
					freeCPU[j] -= h.reqCPU
					freeMem[j] -= h.reqMem
					used[j], placed = true, true
					break
				}
			}
			if !placed {
				break
			}
			h.scheduled++
		}
		h.pending = h.desired - h.scheduled
	}
	count := 0
	for _, u := range used {
		if u {
			count++
		}
	}
	return max(count, p.minNodes)
}

func ptr(f float64) *float64 { return &f }

func (g *graph) resources(nodeRate, edgeRate []float64, mc *mcResult) []ResourceReport {
	var out []ResourceReport
	add := func(res ResourceReport, load, capacity float64) {
		res.Load, res.Capacity = load, capacity
		if capacity > 0 {
			res.Utilization = ptr(load / capacity)
		} else {
			res.Note = "no capacity available"
		}
		out = append(out, res)
	}

	for _, st := range g.stations {
		if st.lambda == 0 || st.meanS == 0 {
			continue // unused, or does no queued work
		}
		res := ResourceReport{ID: st.id, Node: st.node.m.ID, Kind: st.kind, Unit: "ops/s", critical: st.critical}
		if h := st.host; h != nil && h.auto != nil {
			res.ScalingHeadroom = !h.scaleCapped && h.desired < h.auto.max && h.pending == 0
		}
		add(res, st.lambda, float64(st.servers)*1000/st.meanS)
	}
	for _, n := range g.nodes {
		if n.rateLimit > 0 && nodeRate[n.idx] > 0 {
			add(ResourceReport{ID: n.m.ID + "/rate-limit", Node: n.m.ID, Kind: "rate-limit", Unit: "req/s",
				critical: []string{g.key(n, "rateLimitRps")}}, nodeRate[n.idx], n.rateLimit)
		}
	}

	if mc != nil {
		// Little's law: in-flight = arrival rate × time held.
		for _, h := range g.hosts {
			if h.maxConc == 0 || nodeRate[h.svc.idx] == 0 {
				continue
			}
			add(ResourceReport{ID: h.svc.m.ID + "/concurrency", Node: h.svc.m.ID, Kind: "concurrency", Unit: "in-flight",
				critical: []string{g.key(h.svc, "maxConcurrencyPerReplica")},
				Note:     "waiting for a free slot is not added to latency"},
				nodeRate[h.svc.idx]*mc.meanResidence(h.svc.idx)/1000, float64(h.scheduled*h.maxConc))
		}
		for _, p := range g.connPools {
			inUse := 0.0
			for _, e := range p.edges {
				inUse += edgeRate[e.idx] * mc.meanCallMs(e.idx) / 1000
			}
			if inUse == 0 {
				continue
			}
			add(ResourceReport{ID: p.from.m.ID + "->" + p.to.m.ID + "/pool", Node: p.from.m.ID, Kind: "conn-pool", Unit: "connections",
				critical: []string{p.critical},
				Note:     "waiting for a pooled connection is not added to latency"},
				inUse, float64(p.from.host.scheduled*p.size))
		}
		for _, n := range g.nodes {
			if n.arch != catalog.Datastore {
				continue
			}
			active := 0.0
			for _, e := range n.in {
				active += edgeRate[e.idx] * mc.meanCallMs(e.idx) / 1000
			}
			if active > 0 {
				add(ResourceReport{ID: n.m.ID + "/connections", Node: n.m.ID, Kind: "db-connections", Unit: "connections",
					critical: []string{g.key(n, "maxConnections")}}, active, float64(n.maxConns))
			}
		}
	}
	sortResources(out)
	return out
}

// sortResources orders by utilization, most loaded first; resources without
// capacity sort before everything else.
func sortResources(rs []ResourceReport) {
	sort.SliceStable(rs, func(i, j int) bool {
		ui, uj := rs[i].Utilization, rs[j].Utilization
		if ui == nil || uj == nil {
			return ui == nil && uj != nil
		}
		return *ui > *uj
	})
}

func (g *graph) judge(r *Report) {
	w := g.a.Workload
	t := w.Limits()
	level := VerdictSafe
	rank := map[Verdict]int{VerdictSafe: 0, VerdictWarning: 1, VerdictOverload: 2}
	raise := func(v Verdict, format string, args ...any) {
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
		if rank[v] > rank[level] {
			level = v
		}
	}
	feasible := true
	block := func(by string) {
		if feasible {
			feasible, r.limitedBy = false, by
		}
	}

	for _, res := range r.Resources {
		if res.Utilization == nil {
			raise(VerdictOverload, "%s has no capacity", res.ID)
			block("resource:" + res.ID)
			continue
		}
		switch u := *res.Utilization; {
		case u >= t.Overload:
			raise(VerdictOverload, "%s at %.0f%% utilization (overload threshold %.0f%%)", res.ID, u*100, t.Overload*100)
			block("resource:" + res.ID)
		case u >= t.Warn && !res.ScalingHeadroom:
			raise(VerdictWarning, "%s at %.0f%% utilization (warning threshold %.0f%%)", res.ID, u*100, t.Warn*100)
		}
	}
	if !r.Stable {
		raise(VerdictOverload, "demand exceeds capacity: queues grow without bound")
		block("unstable")
	}
	if r.Latency != nil && w.SLO != nil && w.SLO.P99Ms > 0 {
		switch p99 := r.Latency.P99Ms; {
		case p99 > w.SLO.P99Ms:
			raise(VerdictOverload, "p99 %.1f ms exceeds the SLO of %.0f ms", p99, w.SLO.P99Ms)
			block("slo.p99")
		case p99 > 0.8*w.SLO.P99Ms:
			raise(VerdictWarning, "p99 %.1f ms is within 20%% of the SLO of %.0f ms", p99, w.SLO.P99Ms)
		}
	}
	if budget := w.ErrorBudget(); r.ErrorRate != nil && *r.ErrorRate > budget {
		raise(VerdictOverload, "error rate %.3f%% exceeds the budget of %.3f%%", *r.ErrorRate*100, budget*100)
		block("slo.errorRate")
	}
	for _, is := range r.Issues {
		if is.Severity == SeverityError {
			raise(VerdictOverload, "%s: %s", is.Ref, is.Message)
			block("issue:" + is.Ref)
		}
	}
	r.Verdict, r.feasible = level, feasible
}

// confidence weighs the source quality of each resource's critical parameters
// by how loaded the resource is: defaults on the bottleneck hurt the most.
func (g *graph) confidence(r *Report) Confidence {
	var num, den float64
	var defaults []string
	seen := map[string]bool{}
	for i, res := range r.Resources {
		if len(res.critical) == 0 {
			continue
		}
		u := 1.0
		if res.Utilization != nil {
			u = *res.Utilization
		}
		score := 0.0
		for _, k := range res.critical {
			score += sourceScore[g.params.source(k)]
		}
		score /= float64(len(res.critical))
		weight := math.Max(u, 0.05)
		num += weight * score
		den += weight
		if u >= 0.5 || i < 3 {
			for _, k := range res.critical {
				if g.params.source(k) == catalog.SourceDefaultEstimate && !seen[k] {
					seen[k] = true
					defaults = append(defaults, k)
				}
			}
		}
	}
	c := Confidence{Score: 1, CriticalDefaults: defaults}
	if den > 0 {
		c.Score = num / den
	}
	switch {
	case c.Score >= 0.75:
		c.Level = "high"
	case c.Score >= 0.5:
		c.Level = "medium"
	default:
		c.Level = "low"
	}
	if len(defaults) > 0 {
		c.Summary = fmt.Sprintf("Rough estimate: %d critical parameter(s) still use catalog estimates (%s). Enter load-test measurements to improve accuracy.",
			len(defaults), strings.Join(defaults, ", "))
	} else {
		c.Summary = "Critical parameters come from user input or documented sources."
	}
	return c
}
