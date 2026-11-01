package sim

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

func mustSimulate(t *testing.T, doc string, tl model.Timeline) *Timeline {
	t.Helper()
	a, err := model.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	a.Workload.Timeline = &tl
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Simulate(a, cat)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Validation 9.2: in steady state the time simulation must agree with the
// quick check (and so with the closed-form M/M/1 results).
func TestTimelineMatchesQuickCheckInSteadyState(t *testing.T) {
	doc := singleQueue(50, 10, 1, "")
	out := mustSimulate(t, doc, model.Timeline{DurationS: 20, SamplesPerTick: 20000})
	var mean, p99 float64
	for _, tk := range out.Ticks {
		if tk.Latency == nil || tk.ErrorRate != 0 || tk.Stations[0].Backlog != 0 {
			t.Fatalf("tick %v: want stable, error-free, no backlog: %+v", tk.T, tk)
		}
		mean += tk.Latency.MeanMs / float64(len(out.Ticks))
		p99 += tk.Latency.P99Ms / float64(len(out.Ticks))
	}
	// M/M/1, λ=50/s, μ=100/s: sojourn ~ Exp(50/s).
	within(t, "mean", mean, 20, 0.02)
	within(t, "p99", p99, 1000*math.Log(100)/50, 0.03)

	q := mustRun(t, doc).Target.Latency
	within(t, "mean vs quick check", mean, q.MeanMs, 0.03)
	if load := *out.Ticks[0].Stations[0].Load; math.Abs(load-0.5) > 1e-9 {
		t.Errorf("load = %v, want 0.5", load)
	}
}

func TestTimelineFluidBacklog(t *testing.T) {
	// μ = 100/s, λ = 150/s: the backlog grows by 50 visits per second and
	// new arrivals wait backlog/μ behind it.
	out := mustSimulate(t, singleQueue(150, 10, 1, ""), model.Timeline{DurationS: 10, QueueTimeoutMs: 60000})
	for i, tk := range out.Ticks {
		st := tk.Stations[0]
		within(t, fmt.Sprintf("backlog@%d", i), st.Backlog, 50*float64(i+1), 1e-9)
		within(t, fmt.Sprintf("wait@%d", i), st.WaitMs, 500*float64(i+1), 1e-9)
		within(t, fmt.Sprintf("load@%d", i), *st.Load, 1.5, 1e-9)
	}
	// Arrivals during t=9 wait behind a backlog growing from 4.5 s to 5 s of
	// work: the median waits ~4.75 s, plus a ~7 ms median service time.
	if p50 := out.Ticks[9].Latency.P50Ms; p50 < 4700 || p50 > 4820 {
		t.Errorf("p50 at t=9 = %.0f ms, want ~4757 (median of a 4500-5000 ms wait)", p50)
	}

	// With a 2 s queue timeout the backlog caps at 200 visits and the 50/s
	// excess is dropped: a third of new requests fail.
	out = mustSimulate(t, singleQueue(150, 10, 1, ""), model.Timeline{DurationS: 10, QueueTimeoutMs: 2000, SamplesPerTick: 20000})
	last := out.Ticks[9]
	within(t, "capped backlog", last.Stations[0].Backlog, 200, 1e-9)
	within(t, "error rate", last.ErrorRate, 1.0/3, 0.03)
}

func TestTimelineSpikeScalesOut(t *testing.T) {
	// 1-core pods, 10 ms per request, HPA target 50%: 40 TPS needs 1 pod,
	// 250 TPS needs 5. One pod fits per generic-2c4g node, so scaling out
	// also needs the cluster autoscaler.
	doc := fmt.Sprintf(k8sDoc, `"hpa": {"min": 1, "max": 10, "targetCPU": 50}`, 40)
	doc = strings.Replace(doc, `"max": 1, "clusterAutoscaler": false`, `"max": 10, "clusterAutoscaler": true`, 1)
	out := mustSimulate(t, doc, model.Timeline{DurationS: 400, SamplesPerTick: 500,
		Load: &model.LoadPattern{Kind: model.LoadSpike, PeakTPS: 250, StartS: 10, HoldS: 1000}})

	first := out.Ticks[0]
	if first.Replicas[0] != (ReplicaTick{Desired: 1, Ready: 1}) || first.Pools[0].Ready != 1 {
		t.Fatalf("initial state = %+v / %+v, want 1 pod on 1 node", first.Replicas[0], first.Pools[0])
	}
	// The first sync averages ticks 0-15: 10 at 0.4 cores, 6 saturated at
	// 1 core (the pod cannot report demand it does not serve) = 0.625 cores,
	// 125% of the 0.5-core target, so HPA asks for 2 pods, not 5.
	want := map[string]float64{
		"HPA scales 1 → 2 replicas (CPU at 125% of target)":   15,
		"1 pod(s) Pending: no room on the node pool":          16,
		"cluster autoscaler requests 1 node(s), ready in 90s": 16,
		"1 node(s) ready (2 total)":                           106,
	}
	for _, e := range out.Events {
		if at, ok := want[e.Message]; ok && at == e.T {
			delete(want, e.Message)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing events %v in:\n%v", want, out.Events)
	}
	// While the new pod is Pending, HPA sees one saturated pod plus a pod
	// without metrics and keeps recommending 2 (as Kubernetes does).
	for _, i := range []int{30, 60, 120} {
		if d := out.Ticks[i].Replicas[0].Desired; d != 2 {
			t.Errorf("t=%d: desired = %d, want 2", i, d)
		}
	}
	if r := out.Ticks[125].Replicas[0].Ready; r != 1 {
		t.Errorf("t=125: ready = %d, want 1 (pod still starting)", r)
	}
	if r := out.Ticks[126].Replicas[0].Ready; r != 2 {
		t.Errorf("t=126: ready = %d, want 2 (20 s pod startup after the node)", r)
	}

	// The 30 s queue timeout caps the backlog at 30 s of capacity: 3000
	// visits with one pod, 6000 with two. It first overflows at t=30, when
	// 21 s of 150/s excess exceed 3000.
	peak := 0.0
	for _, tk := range out.Ticks {
		peak = math.Max(peak, tk.Stations[0].Backlog)
	}
	within(t, "peak backlog", peak, 6000, 1e-9)
	if fb := out.Summary.FirstBreachS; fb == nil || *fb != 30 {
		t.Errorf("first SLO breach = %v, want 30 (first drops)", fb)
	}
	if last := out.Ticks[len(out.Ticks)-1]; last.Stations[0].Backlog != 0 || last.ErrorRate != 0 || last.Replicas[0].Ready < 5 {
		t.Errorf("final tick: %+v, want drained with >= 5 ready pods", last)
	}
}

func TestTimelineDeterministic(t *testing.T) {
	doc := singleQueue(80, 10, 1, "")
	tl := model.Timeline{DurationS: 30, Load: &model.LoadPattern{Kind: model.LoadSpike, PeakTPS: 120, StartS: 5, RampS: 5, HoldS: 5}}
	if a, b := mustSimulate(t, doc, tl), mustSimulate(t, doc, tl); !reflect.DeepEqual(a, b) {
		t.Error("same design and seed must give identical timelines")
	}
}
