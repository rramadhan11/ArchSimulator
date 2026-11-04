package sim

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"archsim/engine/model"
)

// probeDoc is the calibration lab's service (one 0.25-CPU pod, 62.5 req/s)
// without autoscaling, overloaded at 80 req/s from t=10.
func probeDoc(probe string) string {
	return `{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "deploy", "kind": "k8s.deployment", "config": {"requests": {"cpu": "250m"}, "limits": {"cpu": "250m"}, "replicas": 1` + probe + `}},
	    {"id": "svc", "kind": "service.generic", "parent": "deploy", "config": {"cpuMsPerReq": 4}}
	  ],
	  "edges": [{"from": "c", "to": "svc", "timeoutMs": 2000}],
	  "workload": {"entry": "c", "targetTps": 30}
	}`
}

func probeTimeline() model.Timeline {
	return model.Timeline{DurationS: 60, SamplesPerTick: 500,
		Load: &model.LoadPattern{Kind: model.LoadStep, PeakTPS: 80, StartS: 10}}
}

// No probe and a TCP probe behave the same: neither fails under load.
func TestProbeNoneAndTCPDoNotFail(t *testing.T) {
	none := mustSimulate(t, probeDoc(""), probeTimeline())
	tcp := mustSimulate(t, probeDoc(`, "readinessProbe": {"kind": "tcp"}`), probeTimeline())
	if !reflect.DeepEqual(none.Ticks, tcp.Ticks) {
		t.Error("a tcp probe must not change the timeline")
	}
}

// The backlog grows by 17.5 req/s against a capacity of 62.5 req/s, so its
// wait grows 0.28 s per second and passes the 1 s timeout at t≈13.6.
// Probing every second, failures at t=13, 14 and 15 take the pod out of the
// Service: new requests fail at once while the pod works off its backlog
// (1.72 s of it), and the next probe passes. Then it starts over.
func TestHTTPProbeTakesOverloadedPodOut(t *testing.T) {
	out := mustSimulate(t, probeDoc(`, "readinessProbe": {"kind": "http", "periodS": 1}`), probeTimeline())
	var outAt, backAt float64 = -1, -1
	for _, e := range out.Events {
		if outAt < 0 && strings.HasPrefix(e.Message, "readiness probe failed 3×") {
			outAt = e.T
		}
		if outAt >= 0 && backAt < 0 && strings.HasPrefix(e.Message, "readiness probe passes") {
			backAt = e.T
		}
	}
	if outAt != 15 || backAt != 16 {
		t.Fatalf("out at %v, back at %v; want 15 and 16: %v", outAt, backAt, out.Events)
	}
	for i := int(outAt) + 1; i <= int(backAt); i++ {
		tk := out.Ticks[i]
		if tk.ErrorRate != 1 {
			t.Errorf("t=%d: error rate %v, want 1 while the pod is out of the Service", i, tk.ErrorRate)
		}
		if prev := out.Ticks[i-1].Stations[0].Backlog; tk.Stations[0].Backlog > prev && !math.IsNaN(prev) {
			t.Errorf("t=%d: backlog grew (%v → %v) without new traffic", i, prev, tk.Stations[0].Backlog)
		}
	}
}

// A pod that fails its readiness probe has no usable CPU metric while it is
// younger than the HPA's CPU initialization period (5 min); an older one
// keeps its metric (Kubernetes' groupPods).
func TestHPAMetricsOfNotReadyPods(t *testing.T) {
	newHost := func(young []float64) *hostState {
		return &hostState{
			h:        &hosting{auto: &autoscale{kind: "hpa", min: 1, max: 10, target: 0.7, basis: 0.25}},
			sc:       &scaler{kube: true, syncS: 15, tolerance: 0.1, windowS: 15, delayS: 15, initS: 300},
			desired:  1,
			ready:    1,
			perPod:   slicesRepeat(0.25, 60), // saturated at the limit
			young:    young,
			notReady: true,
		}
	}
	s := &simState{}
	if _, _, _, ok := s.hpaRecommend(newHost([]float64{0}), 59, 59); ok {
		t.Error("a not-Ready pod started 59 s ago must have no metric")
	}
	if _, _, rec, ok := s.hpaRecommend(newHost(nil), 59, 59); !ok || rec != 2 {
		t.Errorf("an old not-Ready pod keeps its metric: ok %v, rec %d, want 2", ok, rec)
	}
}

func slicesRepeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
