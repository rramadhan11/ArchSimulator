package model

import (
	"math"
	"strings"
	"testing"
)

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`{"version": "0.1", "nodes": [], "edges": [], "workload": {}, "typo": 1}`))
	if err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	a, err := Parse([]byte(`{
	  "version": "0.1",
	  "nodes": [{"id": "a", "kind": "client.web"}, {"id": "a", "kind": "service.go"}],
	  "edges": [{"from": "a", "to": "missing", "probability": 2, "classes": ["nope"]}],
	  "workload": {"entry": "ghost", "targetTps": 0}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	err = a.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"duplicate node id", "does not exist", "probability", "class \"nope\"", "workload.entry", "targetTps"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestSpikeLoadPattern(t *testing.T) {
	w := Workload{TargetTPS: 100, Timeline: &Timeline{Load: &LoadPattern{Kind: LoadSpike, PeakTPS: 500, StartS: 10, RampS: 4, HoldS: 6}}}
	for at, want := range map[float64]float64{0: 100, 9.9: 100, 10: 100, 12: 300, 14: 500, 19.9: 500, 20: 500, 22: 300, 24: 100, 100: 100} {
		if got := w.TPSAt(at); got != want {
			t.Errorf("TPSAt(%v) = %v, want %v", at, got, want)
		}
	}
	w.Timeline.Load.RampS = 0
	if got := w.TPSAt(10); got != 500 {
		t.Errorf("instant spike: TPSAt(10) = %v, want 500", got)
	}
	if got := w.TPSAt(16); got != 100 {
		t.Errorf("instant spike: TPSAt(16) = %v, want 100", got)
	}
}

func TestValidateTimeline(t *testing.T) {
	a := &Architecture{
		Version: SchemaVersion,
		Nodes:   []Node{{ID: "c", Kind: "client.web"}},
		Workload: Workload{Entry: "c", TargetTPS: 1, Timeline: &Timeline{
			DurationS: MaxDurationS + 1, Load: &LoadPattern{Kind: "wave"}}},
	}
	err := a.Validate()
	for _, want := range []string{"durationS", "load.kind"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %s", err, want)
		}
	}
}

func TestLoadPatterns(t *testing.T) {
	cases := []struct {
		p    LoadPattern
		want map[float64]float64
	}{
		{LoadPattern{Kind: LoadRamp, PeakTPS: 500, StartS: 10, RampS: 4}, map[float64]float64{0: 100, 10: 100, 12: 300, 14: 500, 1000: 500}},
		{LoadPattern{Kind: LoadStep, PeakTPS: 500, StartS: 10, HoldS: 5}, map[float64]float64{9: 100, 10: 500, 14.9: 500, 15: 100}},
		{LoadPattern{Kind: LoadStep, PeakTPS: 500, StartS: 10}, map[float64]float64{9: 100, 10: 500, 1000: 500}},
		{LoadPattern{Kind: LoadDiurnal, PeakTPS: 500, StartS: 10, PeriodS: 100}, map[float64]float64{0: 100, 10: 100, 35: 300, 60: 500, 110: 100}},
	}
	for _, c := range cases {
		w := Workload{TargetTPS: 100, Timeline: &Timeline{Load: &c.p}}
		for at, want := range c.want {
			if got := w.TPSAt(at); math.Abs(got-want) > 1e-9 {
				t.Errorf("%s: TPSAt(%v) = %v, want %v", c.p.Kind, at, got, want)
			}
		}
	}
}

func TestValidateResilienceAndFailures(t *testing.T) {
	neg := -1.0
	a := &Architecture{
		Version: SchemaVersion,
		Nodes:   []Node{{ID: "c", Kind: "client.web"}, {ID: "s", Kind: "service.go"}},
		Edges: []Edge{{From: "c", To: "s", Retry: &Retry{Max: MaxRetries + 1, BudgetPercent: &neg},
			CircuitBreaker: &CircuitBreaker{FailureRatePercent: 150}}},
		Workload: Workload{Entry: "c", TargetTPS: 1, Timeline: &Timeline{
			Load: &LoadPattern{Kind: LoadDiurnal, PeakTPS: 2},
			Failures: []Failure{
				{Kind: FailZoneOutage, Target: "s"},
				{Kind: FailLatency, Target: "s"},
				{Kind: FailOutage},
				{Kind: FailOutage, Target: "s", Mode: "slow"},
				{Kind: "meteor", Target: "s"},
			}}},
	}
	err := a.Validate()
	for _, want := range []string{"retry.max", "budgetPercent", "failureRatePercent", "periodS",
		"failures[0]: zone-outage", "failures[1]: latency needs extraMs", "failures[2]: target is required",
		"failures[3]: mode", "failures[4]: kind"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}
