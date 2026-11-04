package sim

import (
	"math"
	"math/rand/v2"
)

// CPU limits under Linux CFS bandwidth control: a pod limited to L cores may
// use L·period ms of CPU in each period (100 ms by default). Its threads run
// at full core speed until that quota is used up, then all of them wait for
// the next period. https://docs.kernel.org/scheduler/sched-bwc.html
//
// Throughput is the same as on L slower cores, but latency is not: at low
// load a request finishes at full speed; near the limit, requests that hit an
// exhausted quota wait for the next period. The queueing formulas cannot
// express that, so the wait distribution of one replica comes from a small
// discrete-event simulation, cached per utilization level.
//
// The simulation queues each replica on its own, as when kube-proxy routes
// connections at random. Unthrottled stations instead share one queue across
// all replicas (Erlang C), as a least-loaded balancer would. So a fractional
// limit adds per-replica queueing on top of the throttling; that difference
// is not resolved (the lab cannot tell the two apart).

// cfsModel describes the replicas of a CPU-limited compute station.
type cfsModel struct {
	threads  int     // per replica, each at full core speed
	burst    float64 // speed of one thread (1 core, times the USL factor)
	quotaMs  float64 // CPU per period per replica: limit × period
	periodMs float64
}

const (
	cfsJobs   = 20000 // recorded per simulation
	cfsWarmup = 2000  // discarded first, so the queue starts in steady state
	// Utilization is rounded to steps of 1/cfsSteps for caching: the
	// simulated load is off by at most 0.125%.
	cfsSteps = 400
)

type cfsKey struct {
	st        *station
	rho, mean int // quantized utilization and log mean CPU per job
	cs2       int
}

// cfsWaits returns waits (queueing plus throttling, excluding the request's
// own CPU time) of the station's arrivals, or nil when the station is not
// CPU-limited or not in a steady state. cs2 is the service-time variability.
func (g *graph) cfsWaits(st *station, cs2 float64) []float64 {
	m := st.cfs
	if m == nil || st.lambda <= 0 || st.servers < m.threads {
		return nil
	}
	replicas := float64(st.servers / m.threads)
	cpuMs := st.work / st.lambda / m.burst            // mean CPU per job at full speed
	capacity := 1000 * m.quotaMs / m.periodMs / cpuMs // jobs/s one replica can serve
	rho := st.lambda / replicas / capacity            // share of the quota used
	key := cfsKey{st: st, rho: int(math.Round(rho * cfsSteps)), mean: int(math.Round(math.Log(cpuMs) * 100)), cs2: int(math.Round(cs2 * 20))}
	if key.rho >= cfsSteps || key.rho <= 0 {
		return nil
	}
	if w, ok := g.cfsCache[key]; ok {
		return w
	}
	lambda := float64(key.rho) / cfsSteps * capacity
	w := simulateCFS(lambda, cpuMs, cs2, m, uint64(key.rho)<<32|uint64(key.mean)&0xffff<<16|uint64(key.cs2)&0xffff)
	if g.cfsCache == nil {
		g.cfsCache = map[cfsKey][]float64{}
	}
	g.cfsCache[key] = w
	return w
}

// simulateCFS runs one replica: Poisson arrivals at lambda per second, jobs
// of meanMs CPU (gamma-distributed with variability cs2) served first-come
// first-served by m.threads threads that share one CFS quota.
func simulateCFS(lambda, meanMs, cs2 float64, m *cfsModel, seed uint64) []float64 {
	s := &sampler{r: rand.New(rand.NewPCG(seed, 0x2545f4914f6cdd1d))}
	type job struct{ arrive, cpu, left float64 }
	var (
		queue   []job
		running []job
		waits   = make([]float64, 0, cfsJobs)
		t       float64
		quota   = m.quotaMs
		period  = m.periodMs * (1 + s.r.Float64()) // arrivals start at a random phase
		arrival = s.r.ExpFloat64() * 1000 / lambda
		done    = 0
	)
	start := func() {
		for len(running) < m.threads && len(queue) > 0 {
			running = append(running, queue[0])
			queue = queue[1:]
		}
	}
	for len(waits) < cfsJobs {
		next := math.Min(arrival, period)
		if quota > 0 {
			for _, j := range running {
				next = math.Min(next, t+j.left)
			}
			if len(running) > 0 {
				next = math.Min(next, t+quota/float64(len(running)))
			}
		}
		if dt := next - t; dt > 0 && quota > 0 && len(running) > 0 {
			for k := range running {
				running[k].left -= dt
			}
			if quota -= dt * float64(len(running)); quota < 1e-9 {
				quota = 0 // used up (also absorbs rounding that would stall time)
			}
		}
		t = next

		kept := running[:0]
		for _, j := range running {
			if j.left > 1e-9 {
				kept = append(kept, j)
				continue
			}
			if done++; done > cfsWarmup {
				waits = append(waits, t-j.arrive-j.cpu)
			}
		}
		running = kept
		if t >= period {
			quota, period = m.quotaMs, period+m.periodMs
		}
		if t >= arrival {
			cpu := s.gamma(meanMs, cs2)
			queue = append(queue, job{arrive: t, cpu: cpu, left: cpu})
			arrival = t + s.r.ExpFloat64()*1000/lambda
		}
		start()
	}
	return waits
}
