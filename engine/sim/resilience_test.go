package sim

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"archsim/engine/model"
)

// stormDoc: api (plenty of capacity) calls dep, which runs at 60% (120 TPS
// of 200/s capacity). %s is extra edge config on api -> dep.
func stormDoc(edge string) string {
	return fmt.Sprintf(`{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "api", "kind": "service.generic", "config": {"cpuMsPerReq": 1, "ioWaitMs": 0, "replicas": 4, "vcpu": 1}},
	    {"id": "dep", "kind": "service.generic", "config": {"cpuMsPerReq": 10, "ioWaitMs": 0, "replicas": 2, "vcpu": 1}}
	  ],
	  "edges": [
	    {"from": "c", "to": "api", "latencyMs": 0, "payloadReqKB": 0, "payloadRespKB": 0},
	    {"id": "call", "from": "api", "to": "dep", "latencyMs": 0, "payloadReqKB": 0, "payloadRespKB": 0, "timeoutMs": 500 %s}
	  ],
	  "workload": {"entry": "c", "targetTps": 120}
	}`, edge)
}

var hang20s = []model.Failure{{Kind: model.FailOutage, Target: "dep", AtS: 10, DurationS: 20, Mode: model.ModeHang}}

func stormTimeline() model.Timeline {
	return model.Timeline{DurationS: 90, SamplesPerTick: 2000, Failures: hang20s}
}

func hasEvent(out *Timeline, t float64, ref, msg string) bool {
	for _, e := range out.Events {
		if e.T == t && e.Ref == ref && strings.HasPrefix(e.Message, msg) {
			return true
		}
	}
	return false
}

func TestRetryStormIsMetastable(t *testing.T) {
	out := mustSimulate(t, stormDoc(`, "retry": {"max": 3, "backoffMs": 50}`), stormTimeline())
	before, during, after := out.Ticks[5], out.Ticks[25], out.Ticks[89]
	if before.ErrorRate != 0 || before.Edges[0].Retries != 0 {
		t.Fatalf("before the outage: %+v, want healthy", before)
	}
	// Every attempt times out, so each first attempt is followed by 3 retries.
	within(t, "attempts during outage", during.Edges[0].Attempts, 480, 0.01)
	if during.ErrorRate != 1 {
		t.Errorf("error rate during outage = %v, want 1", during.ErrorRate)
	}
	// After the outage dep receives 480 attempts/s for 200/s of capacity:
	// its queue outgrows the 500 ms timeout, every attempt still fails, and
	// the storm sustains itself long after the trigger is gone.
	if after.ErrorRate < 0.99 || after.Edges[0].Attempts < 470 {
		t.Errorf("60 s after recovery: error %.3f, attempts %.0f/s; want a sustained storm", after.ErrorRate, after.Edges[0].Attempts)
	}
}

func TestRetryBudgetEndsTheStorm(t *testing.T) {
	out := mustSimulate(t, stormDoc(`, "retry": {"max": 3, "backoffMs": 50, "budgetPercent": 10}`), stormTimeline())
	during, after := out.Ticks[25], out.Ticks[60]
	within(t, "attempts during outage", during.Edges[0].Attempts, 132, 0.01)
	if after.ErrorRate != 0 || after.Edges[0].Retries != 0 {
		t.Errorf("30 s after recovery: %+v, want healthy", after)
	}
	// The first failures are sampled at t=10; their retries are due at t=11.
	if !hasEvent(out, 11, "call", "retry budget exhausted") {
		t.Errorf("missing budget event in %v", out.Events)
	}
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	out := mustSimulate(t, stormDoc(`, "retry": {"max": 3, "backoffMs": 50},
	  "circuitBreaker": {"minCalls": 20, "openS": 5}`), stormTimeline())
	for _, want := range []struct {
		t   float64
		msg string
	}{
		// The 10 s window still holds the healthy calls from before the
		// outage; growing retries push the failed share past 50% at t=13.
		{13, "circuit breaker opens: 62% of 1920 calls"},
		{18, "circuit breaker half-open"},
		{18, "circuit breaker re-opens"}, // dep still hangs
		{28, "circuit breaker re-opens"},
		{33, "circuit breaker closes"},
	} {
		if !hasEvent(out, want.t, "call", want.msg) {
			t.Errorf("missing %q at t=%v in %v", want.msg, want.t, out.Events)
		}
	}
	if open := out.Ticks[15].Edges[0]; open.Breaker != breakerOpen || open.Attempts != 0 {
		t.Errorf("t=15: %+v, want open with no calls to dep", open)
	}
	if tk := out.Ticks[40]; tk.ErrorRate != 0 || tk.Edges[0].Attempts != 120 {
		t.Errorf("t=40: error %v, attempts %v; want recovered at 120/s", tk.ErrorRate, tk.Edges[0].Attempts)
	}
}

func TestNodeLossIsReplaced(t *testing.T) {
	doc := fmt.Sprintf(k8sDoc, `"replicas": 2`, 10)
	doc = strings.Replace(doc, `"min": 1, "max": 1`, `"min": 2, "max": 2`, 1)
	out := mustSimulate(t, doc, model.Timeline{DurationS: 130, SamplesPerTick: 200,
		Failures: []model.Failure{{Kind: model.FailInstanceLoss, Target: "pool", AtS: 10}}})
	check := func(at int, nodes, pods int) {
		t.Helper()
		if tk := out.Ticks[at]; tk.Pools[0].Ready != nodes || tk.Replicas[0].Ready != pods {
			t.Errorf("t=%d: %d nodes / %d pods ready, want %d / %d", at, tk.Pools[0].Ready, tk.Replicas[0].Ready, nodes, pods)
		}
	}
	check(9, 2, 2)
	check(10, 1, 1)  // one pod per node: the pod on the lost node dies
	check(100, 2, 1) // replacement node after provisionS 90
	check(119, 2, 1) // its pod is starting (podStartupS 20)
	check(120, 2, 2)
}

func TestUnmanagedInstancesReturnAfterFailure(t *testing.T) {
	out := mustSimulate(t, singleQueue(50, 10, 3, ""), model.Timeline{DurationS: 20, SamplesPerTick: 200,
		Failures: []model.Failure{{Kind: model.FailInstanceLoss, Target: "svc", AtS: 5, DurationS: 10, Count: 2}}})
	// Instances restarted at t=15 (no startup delay) serve from the next tick.
	for at, want := range map[int]int{4: 3, 5: 1, 15: 1, 16: 3} {
		if got := out.Ticks[at].Replicas[0].Ready; got != want {
			t.Errorf("t=%d: ready = %d, want %d", at, got, want)
		}
	}
}

func TestZoneOutageTakesOneZoneOfNodes(t *testing.T) {
	doc := fmt.Sprintf(k8sDoc, `"replicas": 3`, 10)
	doc = strings.Replace(doc, `"min": 1, "max": 1`, `"min": 3, "max": 3`, 1)
	out := mustSimulate(t, doc, model.Timeline{DurationS: 15, SamplesPerTick: 200,
		Failures: []model.Failure{{Kind: model.FailZoneOutage, AtS: 5}}})
	if tk := out.Ticks[5]; tk.Pools[0].Ready != 2 || tk.Replicas[0].Ready != 2 {
		t.Errorf("t=5: %+v / %+v, want 2 nodes and 2 pods left", tk.Pools[0], tk.Replicas[0])
	}
}

func TestFailoverAndLatencyInjection(t *testing.T) {
	doc := `{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "svc", "kind": "service.generic", "config": {"cpuMsPerReq": 1, "ioWaitMs": 0, "replicas": 2, "vcpu": 1}},
	    {"id": "pg", "kind": "db.postgres"}
	  ],
	  "edges": [
	    {"from": "c", "to": "svc", "latencyMs": 0, "payloadReqKB": 0, "payloadRespKB": 0},
	    {"id": "q", "from": "svc", "to": "pg", "op": "write", "latencyMs": 0}
	  ],
	  "workload": {"entry": "c", "targetTps": 50}
	}`
	out := mustSimulate(t, doc, model.Timeline{DurationS: 200, SamplesPerTick: 2000, Failures: []model.Failure{
		{Kind: model.FailFailover, Target: "pg", AtS: 10},
		{Kind: model.FailLatency, Target: "q", AtS: 150, DurationS: 10, ExtraMs: 100},
	}})
	// failoverS defaults to 120 s.
	if out.Ticks[9].ErrorRate != 0 || out.Ticks[10].ErrorRate != 1 || out.Ticks[129].ErrorRate != 1 || out.Ticks[130].ErrorRate != 0 {
		t.Errorf("error rate around the failover: %v %v %v %v, want 0 1 1 0",
			out.Ticks[9].ErrorRate, out.Ticks[10].ErrorRate, out.Ticks[129].ErrorRate, out.Ticks[130].ErrorRate)
	}
	within(t, "mean latency added by injection", out.Ticks[155].Latency.MeanMs-out.Ticks[145].Latency.MeanMs, 100, 0.05)
	if f := out.Failures[0]; f.EndS == nil || *f.EndS != 130 {
		t.Errorf("failover span = %+v, want to end at 130", f)
	}
}

func TestClusterAutoscalerScalesDown(t *testing.T) {
	// 250 TPS needs 5 one-pod nodes; from t=1 the load drops to 40 TPS.
	doc := fmt.Sprintf(k8sDoc, `"hpa": {"min": 1, "max": 10, "targetCPU": 50}`, 250)
	doc = strings.Replace(doc, `"max": 1, "clusterAutoscaler": false`, `"max": 10, "clusterAutoscaler": true`, 1)
	out := mustSimulate(t, doc, model.Timeline{DurationS: 1000, SamplesPerTick: 100,
		Load: &model.LoadPattern{Kind: model.LoadStep, PeakTPS: 40, StartS: 1}})
	// HPA keeps 5 replicas for the 300 s stabilization window, then steps
	// down; each surplus node goes 600 s after it became unneeded.
	for _, want := range []struct {
		t   float64
		ref string
		msg string
	}{
		{315, "deploy", "HPA scales 5 → 2"},
		{330, "deploy", "HPA scales 2 → 1"},
		{916, "pool", "cluster autoscaler removes 3 unneeded node(s) (2 left)"},
		{931, "pool", "cluster autoscaler removes 1 unneeded node(s) (1 left)"},
	} {
		if !hasEvent(out, want.t, want.ref, want.msg) {
			t.Errorf("missing %q at t=%v in %v", want.msg, want.t, out.Events)
		}
	}
}

func TestResilienceDeterministic(t *testing.T) {
	doc := stormDoc(`, "retry": {"max": 2, "backoffMs": 300, "budgetPercent": 50}, "circuitBreaker": {"minCalls": 20, "openS": 5}`)
	if a, b := mustSimulate(t, doc, stormTimeline()), mustSimulate(t, doc, stormTimeline()); !reflect.DeepEqual(a, b) {
		t.Error("same design, failures and seed must give identical timelines")
	}
}
