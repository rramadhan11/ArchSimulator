package sim

import (
	"strings"
	"testing"

	"archsim/engine/model"
)

// memDoc: one 0.25-CPU pod of a generic service (50 MiB baseline) with a
// memory limit; perRequestKiB is the memory each queued or in-flight request
// holds.
func memDoc(limit string, perRequestKiB int, tps int) string {
	return `{
	  "version": "0.1",
	  "nodes": [
	    {"id": "c", "kind": "client.web"},
	    {"id": "deploy", "kind": "k8s.deployment", "config": {"requests": {"cpu": "250m", "memory": "32Mi"}, "limits": {"cpu": "250m", "memory": "` + limit + `"}, "replicas": 1}},
	    {"id": "svc", "kind": "service.generic", "parent": "deploy", "config": {"cpuMsPerReq": 4, "memory": {"perRequestKiB": ` + itoa(perRequestKiB) + `}}}
	  ],
	  "edges": [{"from": "c", "to": "svc", "timeoutMs": 2000}],
	  "workload": {"entry": "c", "targetTps": ` + itoa(tps) + `}
	}`
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n)) }

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// At 30 req/s a pod holds ~0.14 requests at a time, so its memory is the
// 50 MiB baseline: 78% of 64 Mi (safe), 89% of 56 Mi (warning: close to
// the limit), and at 50 Mi the pods are OOMKilled (overload).
func TestQuickCheckMemoryAgainstLimit(t *testing.T) {
	for _, c := range []struct {
		limit   string
		verdict Verdict
	}{
		{"64Mi", VerdictSafe},
		{"56Mi", VerdictWarning},
		{"50Mi", VerdictOverload},
	} {
		res := mustRun(t, memDoc(c.limit, 64, 30))
		var mem *ResourceReport
		for i, r := range res.Target.Resources {
			if r.Kind == "memory" {
				mem = &res.Target.Resources[i]
			}
		}
		if mem == nil {
			t.Fatalf("%s: no memory resource in %+v", c.limit, res.Target.Resources)
		}
		within(t, c.limit+" memory (MiB)", mem.Load, 50, 0.001)
		if res.Target.Verdict != c.verdict {
			t.Errorf("%s: verdict %s, want %s (%v)", c.limit, res.Target.Verdict, c.verdict, res.Target.Reasons)
		}
	}
}

// Overloaded at 80 req/s from t=10, the backlog grows ~18 requests a second.
// At 1 MiB per request a 100 Mi limit allows ~50 of them over the 50 MiB
// baseline, so the container is OOMKilled at t=12 and restarts after the
// back-off and startup (30 s), with its queued requests lost.
func TestOOMKillUnderBacklog(t *testing.T) {
	out := mustSimulate(t, memDoc("100Mi", 1024, 30), model.Timeline{DurationS: 60, SamplesPerTick: 500,
		Load: &model.LoadPattern{Kind: model.LoadStep, PeakTPS: 80, StartS: 10}})
	if !hasEvent(out, 12, "deploy", "OOMKilled (memory") || !hasEvent(out, 12, "deploy", "OOMKilled") {
		t.Fatalf("no OOMKilled event at t=12: %v", out.Events)
	}
	found := false
	for _, e := range out.Events {
		if e.T == 12 && strings.Contains(e.Message, "1 container(s) restart in 30s, queued requests are lost") {
			found = true
		}
	}
	if !found {
		t.Errorf("want the restart in 30 s at t=12: %v", out.Events)
	}
	if r := out.Ticks[13].Replicas[0].Ready; r != 0 {
		t.Errorf("t=13: ready = %d, want 0 while restarting", r)
	}
}
