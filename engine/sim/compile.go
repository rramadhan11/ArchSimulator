package sim

import (
	"fmt"
	"math"
	"strings"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// maxVisits bounds the expanded call trees; fan-out over a DAG duplicates
// shared subtrees, so pathological designs are rejected instead of hanging.
const maxVisits = 20000

// graph is an architecture compiled against the catalog. Structure (call
// trees, stations, hosting) is fixed at compile time; evaluate() fills in the
// load-dependent numbers for one TPS value.
type graph struct {
	cat    *catalog.Catalog
	a      *model.Architecture
	nodes  []*gnode
	byID   map[string]*gnode
	edges  []*gedge
	params *paramLog
	issues []Issue // static issues found at compile time
	err    error

	classes   []model.RequestClass
	weights   []float64 // normalized class weights
	trees     []*visit  // root visit per class
	visits    []*visit
	stations  []*station
	hosts     []*hosting
	pools     []*nodePool
	connPools []*connPool
}

// connPool is the client-side connection pool a service keeps to one callee.
// Several edges between the same pair (e.g. read and write) share one pool.
type connPool struct {
	from, to *gnode
	size     int // per caller replica: the largest size configured on its edges
	edges    []*gedge
	critical string
}

type gnode struct {
	m        *model.Node
	comp     *catalog.Component
	arch     catalog.Archetype
	idx      int
	parent   *gnode
	children []*gnode
	out      []*gedge
	in       []*gedge

	// Per-archetype compiled data. Only the fields of the node's archetype are set.
	st          *station // main station: CPU, proxy, cache threads, partitions, DB primary
	replicaSt   *station // datastore read replicas
	host        *hosting // compute_service
	pool        *nodePool
	serviceMs   float64 // per-visit work at speed 1 (archetypes whose cost does not vary per class)
	cs2         float64
	delayMs     float64
	delayCs2    float64
	runtimeMult float64            // compute_service
	opCostMs    map[string]float64 // datastore
	readsTo     string             // datastore
	maxConns    int                // datastore
	hitRatio    float64            // cache
	partitions  int                // queue_stream (0 = managed)
	rateLimit   float64            // external, 0 = unlimited
}

type gedge struct {
	m         *model.Edge
	id        string
	idx       int
	from, to  *gnode
	protoName string
	proto     catalog.Protocol
	netMs     float64 // per call: RTT (+handshakes) + transfer time
	serMs     float64 // serialization CPU per call per side, before runtime multiplier
	calls     float64
	prob      float64
	poolSize  int // connections per caller replica; 0 = unconstrained
	async     bool
}

func (e *gedge) appliesTo(class string) bool {
	if len(e.m.Classes) == 0 {
		return true
	}
	for _, c := range e.m.Classes {
		if c == class {
			return true
		}
	}
	return false
}

// visit is one position of a node in a class's call tree.
type visit struct {
	idx       int // position in graph.visits
	node      *gnode
	class     int
	perReq    float64 // expected visits per request of this class
	async     bool    // reached through an async boundary (not on the request path)
	st        *station
	serviceMs float64 // work at speed 1
	cs2       float64
	delayMs   float64 // pure delay, no queueing
	delayCs2  float64
	groups    [][]*call // sequential groups of parallel calls

	rate float64 // visits/s at the evaluated TPS
}

type call struct {
	e        *gedge
	child    *visit
	parallel bool
}

// station is a queueing resource: c servers of a given speed.
type station struct {
	id       string
	node     *gnode
	kind     string
	servers  int
	speed    float64
	host     *hosting // set for compute stations; servers resolved per evaluation
	critical []string // param keys that determine its capacity (for confidence)

	// per evaluation
	lambda, work, s2 float64
	meanS            float64 // effective mean service time (ms) on one server
	q                QueueStats
	fluidMs          float64 // time simulation: FIFO wait behind the fluid backlog
	offeredLoad      float64 // time simulation: offered work / capacity this tick
}

func (st *station) addCritical(keys ...string) {
	for _, k := range keys {
		found := false
		for _, have := range st.critical {
			if have == k {
				found = true
				break
			}
		}
		if !found {
			st.critical = append(st.critical, k)
		}
	}
}

// hosting resolves how many replicas (and of what size) run a compute service.
type hosting struct {
	svc          *gnode
	host         *gnode // Deployment or VM group; nil when standalone
	pool         *nodePool
	cores        float64 // per replica
	k            int     // servers per replica
	speed        float64 // per server, includes fractional cores and USL
	uslFactor    float64
	fixed        int
	auto         *autoscale
	reqCPU       float64 // cores, for scheduling
	reqMem       float64 // bytes, for scheduling
	maxConc      int     // in-flight requests per replica, 0 = unlimited
	partitionCap int     // consumer parallelism limit from queue partitions, 0 = none

	// per evaluation
	desired, scheduled, pending int
	scaleCapped                 bool // autoscaler wanted more than max
}

type autoscale struct {
	kind     string // hpa | asg
	min, max int
	target   float64 // utilization fraction of basis
	basis    float64 // cores the target is measured against
}

type nodePool struct {
	n           *gnode
	allocCPU    float64
	allocMem    float64
	minNodes    int
	maxNodes    int
	autoscaled  bool
	deployments []*hosting

	nodesNeeded int // per evaluation
}

func (p *nodePool) capacityNodes() int {
	if p.autoscaled {
		return p.maxNodes
	}
	return p.minNodes
}

func compile(a *model.Architecture, cat *catalog.Catalog) (*graph, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	g := &graph{cat: cat, a: a, byID: map[string]*gnode{}, params: newParamLog()}

	for i := range a.Nodes {
		n := &a.Nodes[i]
		comp, ok := cat.Components[n.Kind]
		if !ok {
			return nil, fmt.Errorf("node %q: unknown kind %q", n.ID, n.Kind)
		}
		gn := &gnode{m: n, comp: comp, arch: comp.Archetype, idx: i}
		g.nodes = append(g.nodes, gn)
		g.byID[n.ID] = gn
	}
	for _, n := range g.nodes {
		if n.m.Parent != "" {
			n.parent = g.byID[n.m.Parent]
			n.parent.children = append(n.parent.children, n)
		}
	}
	if err := g.checkPlacement(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range a.Edges {
		g.addEdge(i, seen)
	}
	if g.err != nil {
		return nil, g.err
	}
	if entry := g.byID[a.Workload.Entry]; entry.arch != catalog.Client {
		return nil, fmt.Errorf("workload.entry %q must be a client node, got %s", entry.m.ID, entry.arch)
	}

	// Node pools first: deployments register themselves with their pool.
	for _, n := range g.nodes {
		if n.arch == catalog.NodePool {
			g.buildNodePool(n)
		}
	}
	for _, n := range g.nodes {
		g.buildNode(n)
	}
	g.resolveEdgeProbabilities()
	g.applyPartitionCaps()
	g.groupConnPools()
	if g.err != nil {
		return nil, g.err
	}

	g.classes = a.Workload.RequestClasses()
	total := 0.0
	for _, c := range g.classes {
		total += c.Weight
	}
	for i, c := range g.classes {
		g.weights = append(g.weights, c.Weight/total)
		root := g.buildTree(i, g.byID[a.Workload.Entry], "", 1, false, nil)
		if g.err != nil {
			return nil, g.err
		}
		g.trees = append(g.trees, root)
	}
	g.reportUnreached()
	for _, e := range g.edges {
		if e.m.Retry != nil && e.m.Retry.Max > 0 {
			g.issues = append(g.issues, Issue{Severity: SeverityInfo, Ref: e.id,
				Message: "retry policy is not modeled yet (quick check and time simulation treat a failed call as final)"})
		}
	}
	return g, nil
}

func (g *graph) checkPlacement() error {
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	for _, n := range g.nodes {
		parentArch := catalog.Archetype("")
		if n.parent != nil {
			parentArch = n.parent.arch
		}
		switch n.arch {
		case catalog.ComputeService:
			if n.parent != nil && parentArch != catalog.Orchestrator && parentArch != catalog.RuntimeHost {
				bad("node %q: a service can only be placed in a Deployment or VM group, not %s", n.m.ID, parentArch)
			}
		case catalog.Orchestrator:
			if n.parent != nil && parentArch != catalog.NodePool {
				bad("node %q: a Deployment can only be placed in a node pool, not %s", n.m.ID, parentArch)
			}
		default:
			if n.parent != nil {
				bad("node %q (%s) cannot be nested inside %q", n.m.ID, n.arch, n.parent.m.ID)
			}
		}
		switch n.arch {
		case catalog.Orchestrator, catalog.RuntimeHost:
			if len(n.children) != 1 || n.children[0].arch != catalog.ComputeService {
				bad("node %q (%s) must contain exactly one service", n.m.ID, n.arch)
			}
		case catalog.NodePool:
			for _, c := range n.children {
				if c.arch != catalog.Orchestrator {
					bad("node pool %q can only contain Deployments, found %q", n.m.ID, c.m.ID)
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid placement:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func callable(a catalog.Archetype) bool {
	switch a {
	case catalog.RuntimeHost, catalog.Orchestrator, catalog.NodePool:
		return false
	}
	return true
}

func (g *graph) addEdge(i int, seen map[string]bool) {
	m := &g.a.Edges[i]
	e := &gedge{m: m, idx: i, from: g.byID[m.From], to: g.byID[m.To]}
	e.id = m.ID
	if e.id == "" {
		e.id = m.From + "->" + m.To
		if seen[e.id] {
			e.id = fmt.Sprintf("%s#%d", e.id, i)
		}
	}
	seen[e.id] = true
	g.edges = append(g.edges, e)
	e.from.out = append(e.from.out, e)
	e.to.in = append(e.to.in, e)

	switch {
	case !callable(e.from.arch) || !callable(e.to.arch):
		g.fail(fmt.Errorf("edge %s: %s/%s nodes cannot send or receive calls; connect the service inside instead",
			e.id, e.from.arch, e.to.arch))
		return
	case e.to.arch == catalog.Client:
		g.fail(fmt.Errorf("edge %s: a client cannot be called", e.id))
		return
	case m.Op != "" && e.to.arch != catalog.Datastore:
		g.fail(fmt.Errorf("edge %s: op is only valid for datastore targets", e.id))
		return
	}

	e.protoName = m.Protocol
	if e.protoName == "" {
		e.protoName = "http1.1+json"
		if p, ok := e.to.comp.Params["protocol"]; ok {
			if s, ok := p.Default.(string); ok {
				e.protoName = s
			}
		}
	}
	proto, ok := g.cat.Protocols[e.protoName]
	if !ok {
		g.fail(fmt.Errorf("edge %s: unknown protocol %q (known: %s)", e.id, e.protoName, g.cat.ProtocolNames()))
		return
	}
	e.proto = proto
	g.params.record(e.id, "protocol", e.protoName, proto.Source)

	ncName, ncSrc := m.NetworkClass, userSource
	if ncName == "" {
		ncName = "same-az"
		if e.from.arch == catalog.Client {
			ncName = "internet"
		}
		ncSrc = catalog.Source{Kind: catalog.SourceDefaultEstimate, Note: "default for this edge position"}
	}
	nc, ok := g.cat.NetworkClasses[ncName]
	if !ok {
		g.fail(fmt.Errorf("edge %s: unknown networkClass %q (known: %s)", e.id, ncName, strings.Join(sortedKeys(g.cat.NetworkClasses), ", ")))
		return
	}
	g.params.record(e.id, "networkClass", ncName, ncSrc)
	rtt, rttSrc := nc.RTTMs, nc.Source
	if m.LatencyMs != nil {
		rtt, rttSrc = *m.LatencyMs, userSource
	}
	g.params.record(e.id, "rttMs", rtt, rttSrc)

	req := g.edgeNum(e, "payloadReqKB", m.PayloadReqKB, 1)
	resp := g.edgeNum(e, "payloadRespKB", m.PayloadRespKB, 1)
	e.netMs = rtt + (req+resp)*8/nc.BandwidthMbps // KB*8 = kbit; kbit / (Mbit/s) = ms
	if m.KeepAlive != nil && !*m.KeepAlive {
		e.netMs += 2 * rtt // new TCP + TLS 1.3 handshake per call
	}
	e.serMs = (req + resp) * proto.SerializeMsPerKB

	e.calls = g.edgeNum(e, "callsPerRequest", m.CallsPerRequest, 1)
	e.prob = 1
	if m.Probability != nil {
		e.prob = *m.Probability
	}
	switch {
	case m.PoolSize != nil:
		e.poolSize = *m.PoolSize
	case !proto.Multiplexed:
		e.poolSize = proto.DefaultPoolSize
	}
	if e.poolSize > 0 && e.from.arch == catalog.ComputeService {
		src := userSource
		if m.PoolSize == nil {
			src = proto.Source
		}
		g.params.record(e.id, "poolSize", e.poolSize, src)
	}
	e.async = m.Async || e.from.arch == catalog.QueueStream
}

func (g *graph) edgeNum(e *gedge, key string, v *float64, def float64) float64 {
	if v != nil {
		g.params.record(e.id, key, *v, userSource)
		return *v
	}
	g.params.record(e.id, key, def, catalog.Source{Kind: catalog.SourceDefaultEstimate})
	return def
}

func (g *graph) resolveEdgeProbabilities() {
	for _, e := range g.edges {
		if e.m.When == "" {
			continue
		}
		cond, id, ok := strings.Cut(e.m.When, ":")
		target := g.byID[id]
		if !ok || (cond != "cache_miss" && cond != "cache_hit") || target == nil || target.arch != catalog.Cache {
			g.fail(fmt.Errorf("edge %s: when must be \"cache_miss:<cacheId>\" or \"cache_hit:<cacheId>\", got %q", e.id, e.m.When))
			continue
		}
		e.prob = target.hitRatio
		if cond == "cache_miss" {
			e.prob = 1 - target.hitRatio
		}
	}
}

func (g *graph) groupConnPools() {
	byPair := map[[2]*gnode]*connPool{}
	for _, e := range g.edges {
		if e.poolSize == 0 || e.from.host == nil {
			continue
		}
		pair := [2]*gnode{e.from, e.to}
		p, ok := byPair[pair]
		if !ok {
			p = &connPool{from: e.from, to: e.to}
			byPair[pair] = p
			g.connPools = append(g.connPools, p)
		}
		p.edges = append(p.edges, e)
		if e.poolSize > p.size {
			p.size, p.critical = e.poolSize, paramKey(e.id, "poolSize")
		}
	}
}

// coreServers splits fractional cores into whole servers: 1.5 cores become two
// servers at 0.75 speed (a single request cannot use more than one core).
func coreServers(cores float64) (int, float64) {
	k := int(math.Max(1, math.Ceil(cores-1e-9)))
	return k, cores / float64(k)
}

func (g *graph) newStation(n *gnode, kind string, servers int, speed float64, critical ...string) *station {
	st := &station{id: n.m.ID + "/" + kind, node: n, kind: kind, servers: servers, speed: speed}
	st.addCritical(critical...)
	g.stations = append(g.stations, st)
	return st
}

func (g *graph) key(n *gnode, param string) string { return paramKey(n.m.ID, param) }

func (g *graph) buildNode(n *gnode) {
	switch n.arch {
	case catalog.EdgeProxy, catalog.Gateway:
		n.delayMs = g.nonNeg(n, "delayMs")
		if n.arch == catalog.EdgeProxy && g.boolean(n, "managed") {
			return
		}
		k, speed := coreServers(g.positive(n, "vcpu"))
		instances := g.count(n, "instances", 1)
		mult := 1.0
		crit := []string{g.key(n, "rpsPerVcpu")}
		if n.arch == catalog.Gateway {
			mults := g.numMap(n, "pluginMultipliers")
			for _, p := range g.strList(n, "plugins") {
				m, ok := mults[p]
				if !ok {
					g.fail(fmt.Errorf("node %q: plugin %q has no entry in pluginMultipliers (known: %s)", n.m.ID, p, strings.Join(sortedKeys(mults), ", ")))
					return
				}
				mult *= m
			}
			crit = append(crit, g.key(n, "pluginMultipliers"))
		}
		n.serviceMs = 1000 / (g.positive(n, "rpsPerVcpu") * mult)
		n.cs2 = g.nonNeg(n, "serviceCs2")
		n.st = g.newStation(n, "proxy", instances*k, speed, crit...)

	case catalog.ComputeService:
		g.buildCompute(n)

	case catalog.Datastore:
		in := g.instance(n)
		k, speed := coreServers(in.VCPU)
		n.opCostMs = g.numMap(n, "opCostMs")
		crit := g.key(n, "opCostMs")
		n.st = g.newStation(n, "db-primary", k, speed, crit)
		if r := g.count(n, "readReplicas", 0); r > 0 {
			n.replicaSt = g.newStation(n, "db-replicas", r*k, speed, crit)
		}
		n.readsTo = g.str(n, "readsTo")
		if n.readsTo != "replicas" && n.readsTo != "primary" {
			g.fail(fmt.Errorf("node %q: readsTo must be replicas or primary, got %q", n.m.ID, n.readsTo))
		}
		n.delayMs = g.nonNeg(n, "commitLatencyMs")
		n.cs2 = g.nonNeg(n, "serviceCs2")
		n.maxConns = g.count(n, "maxConnections", 1)

	case catalog.Cache:
		servers := g.count(n, "nodes", 1) * g.count(n, "threadsPerNode", 1)
		n.serviceMs = 1000 / g.positive(n, "opsPerSecPerThread")
		n.cs2 = g.nonNeg(n, "serviceCs2")
		n.hitRatio = g.num(n, "hitRatio")
		if n.hitRatio < 0 || n.hitRatio > 1 {
			g.fail(fmt.Errorf("node %q: hitRatio must be within [0, 1]", n.m.ID))
		}
		n.st = g.newStation(n, "cache", servers, 1, g.key(n, "opsPerSecPerThread"))

	case catalog.QueueStream:
		n.delayMs = g.nonNeg(n, "publishLatencyMs")
		if g.boolean(n, "managed") {
			return
		}
		n.partitions = g.count(n, "partitions", 1)
		n.serviceMs = 1000 / g.positive(n, "msgsPerSecPerPartition")
		n.cs2 = g.nonNeg(n, "serviceCs2")
		n.st = g.newStation(n, "partitions", n.partitions, 1, g.key(n, "msgsPerSecPerPartition"))

	case catalog.External:
		n.delayMs = g.nonNeg(n, "latencyMs")
		n.delayCs2 = g.nonNeg(n, "latencyCs2")
		n.rateLimit = g.nonNeg(n, "rateLimitRps")
	}
}

func (g *graph) buildCompute(n *gnode) {
	h := &hosting{svc: n, host: n.parent}
	p := n.parent
	switch {
	case p == nil:
		h.cores = g.positive(n, "vcpu")
		h.fixed = g.count(n, "replicas", 1)
	case p.arch == catalog.Orchestrator:
		h.reqCPU = g.cpuCores(p, "requests.cpu")
		h.reqMem = g.memBytes(p, "requests.memory")
		limit := g.cpuCores(p, "limits.cpu")
		if !(h.reqCPU > 0) {
			g.fail(fmt.Errorf("deployment %q: requests.cpu must be > 0", p.m.ID))
			return
		}
		if limit > 0 && limit < h.reqCPU {
			g.fail(fmt.Errorf("deployment %q: limits.cpu (%v) is below requests.cpu (%v)", p.m.ID, limit, h.reqCPU))
			return
		}
		h.cores = h.reqCPU
		if limit > 0 {
			h.cores = limit
		} else {
			g.issues = append(g.issues, Issue{Severity: SeverityInfo, Ref: p.m.ID,
				Message: "no CPU limit: capacity assumes each pod uses exactly its CPU request (bursting above it is not counted)"})
		}
		if g.has(p, "hpa") {
			h.auto = &autoscale{kind: "hpa", min: g.count(p, "hpa.min", 1), max: g.count(p, "hpa.max", 1),
				target: g.positive(p, "hpa.targetCPU") / 100, basis: h.reqCPU}
		} else {
			h.fixed = g.count(p, "replicas", 0)
		}
		if p.parent != nil {
			h.pool = p.parent.pool
			h.pool.deployments = append(h.pool.deployments, h)
		} else {
			g.issues = append(g.issues, Issue{Severity: SeverityInfo, Ref: p.m.ID,
				Message: "deployment is not inside a node pool: scheduling capacity is assumed unlimited"})
		}
	case p.arch == catalog.RuntimeHost:
		h.cores = g.instance(p).VCPU
		if g.has(p, "autoscaling") {
			h.auto = &autoscale{kind: "asg", min: g.count(p, "autoscaling.min", 1), max: g.count(p, "autoscaling.max", 1),
				target: g.positive(p, "autoscaling.targetCPU") / 100, basis: h.cores}
		} else {
			h.fixed = g.count(p, "count", 1)
		}
	}
	if h.auto != nil && h.auto.max < h.auto.min {
		g.fail(fmt.Errorf("node %q: autoscaling max (%d) is below min (%d)", p.m.ID, h.auto.max, h.auto.min))
		return
	}

	h.k, h.speed = coreServers(h.cores)
	if w := g.count(n, "workersPerReplica", 0); w > 0 {
		h.k, h.speed = w, math.Min(1, h.cores/float64(w))
	}
	sigma, kappa := g.nonNeg(n, "usl.sigma"), g.nonNeg(n, "usl.kappa")
	nk := float64(h.k)
	h.uslFactor = 1 / (1 + sigma*(nk-1) + kappa*nk*(nk-1))
	h.speed *= h.uslFactor
	h.maxConc = g.count(n, "maxConcurrencyPerReplica", 0)

	n.runtimeMult = g.positive(n, "runtimeMultiplier")
	n.cs2 = g.nonNeg(n, "serviceCs2")
	n.host = h
	n.st = g.newStation(n, "cpu", 0, h.speed, g.key(n, "runtimeMultiplier"))
	n.st.host = h
	g.hosts = append(g.hosts, h)
}

// GKE allocatable formula (also a reasonable proxy for EKS/AKS).
// https://cloud.google.com/kubernetes-engine/docs/concepts/plan-node-sizes
func allocatable(in catalog.Instance) (cpu, memBytes float64) {
	reserved := tieredReserve(in.VCPU, []float64{1, 1, 2, math.Inf(1)}, []float64{0.06, 0.01, 0.005, 0.0025})

	gib := in.MemoryGiB
	memReservedGiB := 0.25
	if gib >= 1 {
		memReservedGiB = tieredReserve(gib, []float64{4, 4, 8, 112, math.Inf(1)}, []float64{0.25, 0.20, 0.10, 0.06, 0.02})
	}
	const evictionGiB = 100.0 / 1024
	return in.VCPU - reserved, (gib - memReservedGiB - evictionGiB) * (1 << 30)
}

// tieredReserve applies rates[i] to the slice of total that falls in tier i.
func tieredReserve(total float64, sizes, rates []float64) float64 {
	reserved, left := 0.0, total
	for i, size := range sizes {
		if left <= 0 {
			break
		}
		part := math.Min(left, size)
		reserved += part * rates[i]
		left -= part
	}
	return reserved
}

func (g *graph) buildNodePool(n *gnode) {
	in := g.instance(n)
	p := &nodePool{n: n, minNodes: g.count(n, "min", 0), maxNodes: g.count(n, "max", 1), autoscaled: g.boolean(n, "clusterAutoscaler")}
	if p.maxNodes < p.minNodes {
		g.fail(fmt.Errorf("node pool %q: max (%d) is below min (%d)", n.m.ID, p.maxNodes, p.minNodes))
	}
	p.allocCPU, p.allocMem = allocatable(in)
	g.params.record(n.m.ID, "allocatablePerNode",
		fmt.Sprintf("%.2f vCPU / %.2f GiB", p.allocCPU, p.allocMem/(1<<30)),
		catalog.Source{Kind: catalog.SourceVendorDocs, URL: "https://cloud.google.com/kubernetes-engine/docs/concepts/plan-node-sizes",
			Note: "GKE reservation formula; EKS/AKS differ slightly; DaemonSet requests are not deducted"})
	n.pool = p
	g.pools = append(g.pools, p)
}

// applyPartitionCaps limits consumer parallelism: a consumer group cannot use
// more replicas than the partitions it reads from.
func (g *graph) applyPartitionCaps() {
	for _, h := range g.hosts {
		total, onlyPartitioned := 0, len(h.svc.in) > 0
		for _, e := range h.svc.in {
			if e.from.arch != catalog.QueueStream || e.from.partitions == 0 {
				onlyPartitioned = false
				break
			}
			total += e.from.partitions
		}
		if onlyPartitioned {
			h.partitionCap = total
		}
	}
}

func (g *graph) buildTree(class int, n *gnode, op string, perReq float64, async bool, path []*gnode) *visit {
	for _, p := range path {
		if p == n {
			names := make([]string, 0, len(path)+1)
			for _, q := range path {
				names = append(names, q.m.ID)
			}
			g.fail(fmt.Errorf("call cycle %s -> %s: quick check requires an acyclic call graph", strings.Join(names, " -> "), n.m.ID))
			return nil
		}
	}
	if len(g.visits) >= maxVisits {
		g.fail(fmt.Errorf("call tree exceeds %d visits: fan-out is too large to expand", maxVisits))
		return nil
	}
	className := g.classes[class].Name
	v := &visit{idx: len(g.visits), node: n, class: class, perReq: perReq, async: async}
	g.initVisit(v, className, op)
	g.visits = append(g.visits, v)

	path = append(path, n)
	groupIdx := map[string]int{}
	for _, e := range n.out {
		if !e.appliesTo(className) {
			continue
		}
		child := g.buildTree(class, e.to, e.m.Op, perReq*e.calls*e.prob, async || e.async, path)
		if child == nil {
			return nil
		}
		c := &call{e: e, child: child, parallel: e.m.ParallelGroup != ""}
		if name := e.m.ParallelGroup; name != "" {
			if i, ok := groupIdx[name]; ok {
				v.groups[i] = append(v.groups[i], c)
				continue
			}
			groupIdx[name] = len(v.groups)
		}
		v.groups = append(v.groups, []*call{c})
	}
	// Serialization CPU is paid by services on both ends of every call.
	for _, grp := range v.groups {
		for _, c := range grp {
			if n.arch == catalog.ComputeService {
				v.serviceMs += c.e.calls * c.e.prob * c.e.serMs * n.runtimeMult
			}
			if c.child.node.arch == catalog.ComputeService {
				c.child.serviceMs += c.e.serMs * c.child.node.runtimeMult
			}
		}
	}
	return v
}

func (g *graph) initVisit(v *visit, class, op string) {
	n := v.node
	switch n.arch {
	case catalog.EdgeProxy, catalog.Gateway, catalog.Cache, catalog.QueueStream:
		v.st, v.serviceMs, v.cs2, v.delayMs = n.st, n.serviceMs, n.cs2, n.delayMs
		v.delayCs2 = 0.5
	case catalog.External:
		v.delayMs, v.delayCs2 = n.delayMs, n.delayCs2
	case catalog.ComputeService:
		cpu, io, keys := g.computeCost(n, class)
		v.st, v.cs2 = n.st, n.cs2
		v.serviceMs = cpu * n.runtimeMult
		v.delayMs, v.delayCs2 = io, 1
		n.st.addCritical(keys...)
	case catalog.Datastore:
		if op == "" {
			op = "read"
		}
		cost, ok := n.opCostMs[op]
		if !ok {
			g.fail(fmt.Errorf("node %q: op %q has no entry in opCostMs (known: %s)", n.m.ID, op, strings.Join(sortedKeys(n.opCostMs), ", ")))
			return
		}
		v.serviceMs, v.cs2, v.st = cost, n.cs2, n.st
		switch {
		case op == "write":
			v.delayMs, v.delayCs2 = n.delayMs, 1
		case n.replicaSt != nil && n.readsTo == "replicas":
			v.st = n.replicaSt
		}
	}
}

// computeCost resolves CPU and IO-wait per request for a class: per-class
// config, then node config, then the selected profile.
func (g *graph) computeCost(n *gnode, class string) (cpuMs, ioMs float64, keys []string) {
	profileName := g.str(n, "profile")
	prof, ok := g.cat.Profiles[profileName]
	if !ok {
		g.fail(fmt.Errorf("node %q: unknown profile %q (known: %s)", n.m.ID, profileName, strings.Join(sortedKeys(g.cat.Profiles), ", ")))
		return 0, 0, nil
	}
	pick := func(field string, fromProfile float64) (float64, string) {
		for _, path := range []string{"classes." + class + "." + field, field} {
			if raw, ok := configValue(n.m.Config, path); ok {
				f, err := toFloat(raw)
				if err != nil || f < 0 {
					g.fail(fmt.Errorf("node %q: %s must be a number >= 0", n.m.ID, path))
				}
				return f, g.params.record(n.m.ID, path, f, userSource)
			}
		}
		src := prof.Source
		src.Note = "profile " + profileName + ": " + src.Note
		return fromProfile, g.params.record(n.m.ID, field, fromProfile, src)
	}
	cpuMs, cpuKey := pick("cpuMsPerReq", prof.CPUMsPerReq)
	ioMs, _ = pick("ioWaitMs", prof.IOWaitMs)
	return cpuMs, ioMs, []string{cpuKey}
}

func (g *graph) reportUnreached() {
	reached := map[*gnode]bool{}
	for _, v := range g.visits {
		reached[v.node] = true
	}
	for _, n := range g.nodes {
		if callable(n.arch) && !reached[n] && n.m.ID != g.a.Workload.Entry {
			g.issues = append(g.issues, Issue{Severity: SeverityWarning, Ref: n.m.ID,
				Message: "not reachable from the workload entry; it is not simulated"})
		}
	}
}
