package sim

import (
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"archsim/engine/catalog"
	"archsim/engine/model"
)

// With a quota that never runs out, the simulation is a plain M/M/1 queue:
// mean wait ρ/(1-ρ)·S.
func TestCFSWithoutThrottlingIsMM1(t *testing.T) {
	m := &cfsModel{threads: 1, burst: 1, quotaMs: 100, periodMs: 100}
	waits := simulateCFS(100, 5, 1, m, 1) // λ = 100/s, S = 5 ms: ρ = 0.5
	mean := 0.0
	for _, w := range waits {
		mean += w / float64(len(waits))
	}
	within(t, "mean wait (ms)", mean, 5, 0.08)
}

// A pod limited to 0.25 CPU runs a 4 ms request at full speed: at low load
// almost nothing waits (the lab measured p50 4.4 ms). Near the limit, requests
// that find the 25 ms quota used up wait for the next 100 ms period.
func TestCFSThrottling(t *testing.T) {
	m := &cfsModel{threads: 1, burst: 1, quotaMs: 25, periodMs: 100}
	low := simulateCFS(10, 4, 0, m, 1) // 16% of the quota
	slices.Sort(low)
	if p50, p99 := low[len(low)/2], low[len(low)*99/100]; p50 > 0.5 || p99 > 10 {
		t.Errorf("16%%: wait p50 %.1f ms, p99 %.1f ms; want ~0 and a short tail", p50, p99)
	}
	high := simulateCFS(55, 4, 0, m, 1) // 88% of the quota
	slices.Sort(high)
	if p99 := high[len(high)*99/100]; p99 < 75 {
		t.Errorf("88%%: wait p99 %.1f ms, want at least one throttled period (75 ms)", p99)
	}
	if !reflect.DeepEqual(simulateCFS(55, 4, 0, m, 1), simulateCFS(55, 4, 0, m, 1)) {
		t.Error("same inputs and seed must give the same waits")
	}
}

// The lab's service at 30 req/s on one 0.25-CPU pod: before CFS was modeled
// the quick check put p50 at 16 ms (a core 4× slower); the lab measured 4.4.
func TestLabDesignLatencyUnderCPULimit(t *testing.T) {
	data, err := os.ReadFile("../../lab/design.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := model.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := catalog.Load()
	res, err := Run(a, cat)
	if err != nil {
		t.Fatal(err)
	}
	if p50 := res.Target.Latency.P50Ms; p50 < 3.5 || p50 > 5 {
		t.Errorf("p50 = %.1f ms, want ~4 (full core speed, rarely throttled)", p50)
	}
}

// At very low load (as the max-TPS search probes) the simulation skips idle
// periods instead of stepping through each one.
func TestCFSLowLoadIsFast(t *testing.T) {
	m := &cfsModel{threads: 1, burst: 1, quotaMs: 25, periodMs: 100}
	start := time.Now()
	waits := simulateCFS(0.01, 4, 0, m, 1) // one 4 ms request per 100 s
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
	for _, w := range waits {
		if w > 1e-9 {
			t.Fatalf("a request well under the quota on an idle pod must not wait, got %v ms", w)
		}
	}
}
