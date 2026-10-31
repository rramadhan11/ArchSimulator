package sim

import (
	"math"
	"math/rand/v2"
)

// ErlangC is the probability that an arrival has to wait in an M/M/c queue
// with c servers and offered load a = λ/μ. It uses the Erlang B recurrence,
// which is numerically stable for large c. Returns 1 when the queue is unstable.
func ErlangC(c int, a float64) float64 {
	if c <= 0 {
		return 1
	}
	rho := a / float64(c)
	if rho >= 1 {
		return 1
	}
	b := 1.0
	for k := 1; k <= c; k++ {
		b = a * b / (float64(k) + a*b)
	}
	return b / (1 - rho*(1-b))
}

// QueueStats describes the steady state of a G/G/c station approximated as
// M/M/c with the Kingman / Allen-Cunneen correction for service variability.
type QueueStats struct {
	Rho float64 // utilization λ/(cμ)
	// PWait is the probability an arrival waits (Erlang C).
	PWait float64
	// CondWaitMs is the mean wait of arrivals that do wait; waits are modeled
	// as exponential with this mean, so P(Wq > t) = PWait·exp(-t/CondWaitMs).
	CondWaitMs float64
}

// MeanWaitMs is the unconditional mean queueing delay Wq.
func (q QueueStats) MeanWaitMs() float64 { return q.PWait * q.CondWaitMs }

// Stable reports whether the queue reaches a steady state.
func (q QueueStats) Stable() bool { return q.Rho < 1 }

// Queue computes station statistics. lambda is arrivals/s, meanServiceMs the
// mean service time on one server, cs2 the squared coefficient of variation
// of service time. Arrivals are assumed Poisson (ca² = 1).
func Queue(lambda, meanServiceMs, cs2 float64, servers int) QueueStats {
	if lambda <= 0 || meanServiceMs <= 0 {
		return QueueStats{}
	}
	if servers <= 0 {
		return QueueStats{Rho: math.Inf(1), PWait: 1}
	}
	mu := 1000 / meanServiceMs // per server, per second
	capacity := float64(servers) * mu
	rho := lambda / capacity
	if rho >= 1 {
		return QueueStats{Rho: rho, PWait: 1}
	}
	const ca2 = 1.0
	return QueueStats{
		Rho:        rho,
		PWait:      ErlangC(servers, lambda/mu),
		CondWaitMs: 1000 * (ca2 + cs2) / 2 / (capacity - lambda),
	}
}

// sampler draws the random variates of the Monte Carlo pass. It is seeded so
// that the same design and seed always produce identical results.
type sampler struct{ r *rand.Rand }

func newSampler(seed uint64) *sampler {
	return &sampler{r: rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))}
}

func (s *sampler) bernoulli(p float64) bool {
	if p >= 1 {
		return true
	}
	if p <= 0 {
		return false
	}
	return s.r.Float64() < p
}

// count turns a mean call count (e.g. 2.5) into an integer draw with that mean.
func (s *sampler) count(mean float64) int {
	whole := math.Floor(mean)
	n := int(whole)
	if s.bernoulli(mean - whole) {
		n++
	}
	return n
}

func (s *sampler) exp(mean float64) float64 {
	if mean <= 0 {
		return 0
	}
	return s.r.ExpFloat64() * mean
}

// wait draws a queueing delay for a station.
func (s *sampler) wait(q QueueStats) float64 {
	if s.bernoulli(q.PWait) {
		return s.exp(q.CondWaitMs)
	}
	return 0
}

// gamma draws a duration with the given mean and squared coefficient of
// variation: cs2 = 0 is deterministic, 1 is exponential, >1 is heavy-ish.
func (s *sampler) gamma(mean, cs2 float64) float64 {
	switch {
	case mean <= 0:
		return 0
	case cs2 <= 0:
		return mean
	case cs2 == 1:
		return s.exp(mean)
	}
	return s.gammaShape(1/cs2) * mean * cs2
}

// gammaShape draws Gamma(k, 1) with Marsaglia & Tsang (2000).
func (s *sampler) gammaShape(k float64) float64 {
	if k < 1 {
		return s.gammaShape(k+1) * math.Pow(s.r.Float64(), 1/k)
	}
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := s.r.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := s.r.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
