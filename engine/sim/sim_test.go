package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

func within(t *testing.T, name string, got, want, relTol float64) {
	t.Helper()
	if math.Abs(got-want) > relTol*math.Abs(want) {
		t.Errorf("%s = %.4f, want %.4f ±%.1f%%", name, got, want, relTol*100)
	}
}

func mustRun(t *testing.T, doc string) *Result {
	t.Helper()
	a, err := model.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(a, cat)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// singleQueue is client -> one generic service with no network or
// serialization cost: an M/M/c queue with c = replicas and S = serviceMs.
func singleQueue(tps, serviceMs float64, replicas int, extraSvcConfig string) string {
	return fmt.Sprintf(`{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "svc", "kind": "service.generic",
	     "config": {"cpuMsPerReq": %v, "ioWaitMs": 0, "replicas": %d, "vcpu": 1 %s}}
	  ],
	  "edges": [{"from": "c", "to": "svc", "latencyMs": 0, "payloadReqKB": 0, "payloadRespKB": 0}],
	  "workload": {"entry": "c", "targetTps": %v, "samples": 100000}
	}`, serviceMs, replicas, extraSvcConfig, tps)
}

func TestErlangC(t *testing.T) {
	within(t, "C(1, 0.5)", ErlangC(1, 0.5), 0.5, 1e-9)
	within(t, "C(2, 1.5)", ErlangC(2, 1.5), 9.0/14, 1e-9) // closed form
	if ErlangC(2, 2) != 1 {
		t.Error("unstable queue must wait with probability 1")
	}
}

func TestQueueClosedForms(t *testing.T) {
	// M/M/1, λ=50/s, S=10 ms: Wq = ρ/(μ-λ) = 10 ms.
	within(t, "M/M/1 Wq", Queue(50, 10, 1, 1).MeanWaitMs(), 10, 1e-9)
	// M/D/1 (Pollaczek-Khinchine): Wq = ρS/(2(1-ρ)) = 5 ms; Kingman is exact here.
	within(t, "M/D/1 Wq", Queue(50, 10, 0, 1).MeanWaitMs(), 5, 1e-9)
	// M/M/2, λ=150/s, S=10 ms: Wq = C/(cμ-λ) = (9/14)/50 s.
	within(t, "M/M/2 Wq", Queue(150, 10, 1, 2).MeanWaitMs(), 1000*(9.0/14)/50, 1e-9)
	if q := Queue(100, 10, 1, 1); q.Stable() {
		t.Error("ρ = 1 must be unstable")
	}
}

func TestMM1EndToEnd(t *testing.T) {
	res := mustRun(t, singleQueue(50, 10, 1, ""))
	l := res.Target.Latency
	if l == nil {
		t.Fatal("expected a stable result")
	}
	// M/M/1 sojourn time is exponential with rate μ-λ = 50/s.
	within(t, "mean", l.MeanMs, 20, 0.02)
	within(t, "p50", l.P50Ms, 1000*math.Log(2)/50, 0.03)
	within(t, "p99", l.P99Ms, 1000*math.Log(100)/50, 0.04)
	within(t, "utilization", *res.Target.Resources[0].Utilization, 0.5, 1e-9)
}

func TestMMcEndToEnd(t *testing.T) {
	res := mustRun(t, singleQueue(150, 10, 2, ""))
	within(t, "mean", res.Target.Latency.MeanMs, 10+1000*(9.0/14)/50, 0.02)
}

func TestLittlesLaw(t *testing.T) {
	// In-flight requests = λ·W = 50/s × 20 ms = 1.
	res := mustRun(t, singleQueue(50, 10, 1, `, "maxConcurrencyPerReplica": 100`))
	for _, r := range res.Target.Resources {
		if r.Kind == "concurrency" {
			within(t, "in-flight", r.Load, 1, 0.02)
			return
		}
	}
	t.Fatal("concurrency resource missing")
}

func TestMaxSustainableTPS(t *testing.T) {
	// Capacity 100/s; overload threshold 85% → 85 TPS.
	res := mustRun(t, singleQueue(10, 10, 1, ""))
	within(t, "max TPS", *res.MaxSustainableTPS, 85, 0.005)
	if res.LimitedBy != "resource:svc/cpu" {
		t.Errorf("limitedBy = %q", res.LimitedBy)
	}

	// Two calls per request halve it (visit ratio 2).
	doc := strings.Replace(singleQueue(10, 10, 1, ""), `"latencyMs": 0,`, `"latencyMs": 0, "callsPerRequest": 2,`, 1)
	within(t, "max TPS with fan-out 2", *mustRun(t, doc).MaxSustainableTPS, 42.5, 0.005)
}

func TestSLOLimitsMaxTPS(t *testing.T) {
	// M/M/1 with S=10 ms: p99 = ln(100)/(μ-λ) <= 200 ms ⇔ λ <= 100 - 1000·ln(100)/200 ≈ 76.97.
	doc := strings.Replace(singleQueue(10, 10, 1, ""), `"samples": 100000`, `"samples": 100000, "slo": {"p99Ms": 200}`, 1)
	res := mustRun(t, doc)
	within(t, "max TPS", *res.MaxSustainableTPS, 100-1000*math.Log(100)/200, 0.03)
	if res.LimitedBy != "slo.p99" {
		t.Errorf("limitedBy = %q", res.LimitedBy)
	}
}

func TestDeterministic(t *testing.T) {
	doc := singleQueue(60, 10, 1, "")
	a, _ := json.Marshal(mustRun(t, doc))
	b, _ := json.Marshal(mustRun(t, doc))
	if !reflect.DeepEqual(a, b) {
		t.Error("same design and seed must produce identical results")
	}
}

const k8sDoc = `{
  "version": "0.1",
  "nodes": [
    {"id": "c", "kind": "client.web"},
    {"id": "pool", "kind": "k8s.nodepool", "config": {"instanceType": "generic-2c4g", "min": 1, "max": 1, "clusterAutoscaler": false}},
    {"id": "deploy", "kind": "k8s.deployment", "parent": "pool", "config": {"requests": {"cpu": "1", "memory": "1Gi"}, %s}},
    {"id": "svc", "kind": "service.generic", "parent": "deploy", "config": {"cpuMsPerReq": 10}}
  ],
  "edges": [{"from": "c", "to": "svc", "latencyMs": 0, "payloadReqKB": 0, "payloadRespKB": 0}],
  "workload": {"entry": "c", "targetTps": %v, "samples": 5000}
}`

func TestHPASteadyStateReplicas(t *testing.T) {
	// 250 TPS × 10 ms = 2.5 cores; target 50% of a 1-core request → 5 replicas.
	doc := fmt.Sprintf(k8sDoc, `"hpa": {"min": 1, "max": 10, "targetCPU": 50}`, 250)
	doc = strings.Replace(doc, `"max": 1, "clusterAutoscaler": false`, `"max": 10, "clusterAutoscaler": true`, 1)
	rep := mustRun(t, doc).Target.Replicas[0]
	if rep.Desired != 5 || rep.Scheduled != 5 {
		t.Errorf("replicas = %+v, want 5 desired and scheduled", rep)
	}
}

func TestPendingPodsWhenPoolIsFull(t *testing.T) {
	// One 2-vCPU node has ~1.93 allocatable vCPU: only one 1-vCPU pod fits.
	res := mustRun(t, fmt.Sprintf(k8sDoc, `"replicas": 3`, 10))
	rep := res.Target.Replicas[0]
	if rep.Scheduled != 1 || rep.Pending != 2 {
		t.Errorf("replicas = %+v, want 1 scheduled, 2 pending", rep)
	}
	if res.Target.Verdict != VerdictOverload {
		t.Errorf("verdict = %s, want overload because of pending pods", res.Target.Verdict)
	}
}

func TestCacheMissRouting(t *testing.T) {
	doc := `{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "svc", "kind": "service.go"},
	    {"id": "redis", "kind": "cache.redis", "config": {"hitRatio": 0.9}},
	    {"id": "pg", "kind": "db.postgres"}
	  ],
	  "edges": [
	    {"from": "c", "to": "svc"},
	    {"from": "svc", "to": "redis"},
	    {"from": "svc", "to": "pg", "when": "cache_miss:redis"}
	  ],
	  "workload": {"entry": "c", "targetTps": 100, "samples": 2000}
	}`
	for _, h := range mustRun(t, doc).Target.Hops {
		if h.Node == "pg" {
			within(t, "pg visits per request", h.VisitsPerRequest, 0.1, 1e-9)
			return
		}
	}
	t.Fatal("pg hop missing")
}

func TestCycleRejected(t *testing.T) {
	doc := `{
	  "version": "0.1",
	  "nodes": [{"id": "c", "kind": "client.web"}, {"id": "a", "kind": "service.go"}, {"id": "b", "kind": "service.go"}],
	  "edges": [{"from": "c", "to": "a"}, {"from": "a", "to": "b"}, {"from": "b", "to": "a"}],
	  "workload": {"entry": "c", "targetTps": 10}
	}`
	a, _ := model.Parse([]byte(doc))
	cat, _ := catalog.Load()
	if _, err := Run(a, cat); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("err = %v, want a cycle error", err)
	}
}

func TestExamples(t *testing.T) {
	files, _ := filepath.Glob("../examples/*.json")
	if len(files) < 3 {
		t.Fatalf("expected the 3 example designs, found %d", len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			res := mustRun(t, string(data))
			if res.MaxSustainableTPS == nil || *res.MaxSustainableTPS <= 0 {
				t.Errorf("max TPS = %v, want a positive bound", res.MaxSustainableTPS)
			}
			if _, err := json.Marshal(res); err != nil {
				t.Errorf("result is not JSON-encodable: %v", err)
			}
		})
	}
}
