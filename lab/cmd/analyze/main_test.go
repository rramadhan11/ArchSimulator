package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A 3-second run starting at unix 1000: second 1 is slow (p99 900 ms) with
// one failure, and the deployment scales from 1 to 2 at second 1.
func TestAnalyzeSyntheticRun(t *testing.T) {
	dir := t.TempDir()
	var k6 strings.Builder
	k6.WriteString("metric_name,timestamp,metric_value,check,error\n")
	for s, durations := range [][]float64{{10, 12}, {900, 20, 30}, {15}} {
		for i, d := range durations {
			fmt.Fprintf(&k6, "http_reqs,%d,1,,\n", 1000+s)
			fmt.Fprintf(&k6, "http_req_duration,%d,%v,,\n", 1000+s, d)
			failed := 0
			if s == 1 && i == 0 {
				failed = 1
			}
			fmt.Fprintf(&k6, "http_req_failed,%d,%d,,\n", 1000+s, failed)
		}
	}
	write(t, dir, "k6.csv", k6.String())
	write(t, dir, "pods.csv", "999,1,1\n1000,1,1\n1001,,2\n1002,2,2\n")
	write(t, dir, "sim.json", `{"ticks": [
	  {"t": 0, "offeredTps": 2, "errorRate": 0, "latency": {"p99Ms": 11}, "replicas": [{"desired": 1, "ready": 1}]},
	  {"t": 1, "offeredTps": 3, "errorRate": 0.5, "latency": {"p99Ms": 800}, "replicas": [{"desired": 1, "ready": 1}]},
	  {"t": 2, "offeredTps": 1, "errorRate": 0, "latency": {"p99Ms": 14}, "replicas": [{"desired": 2, "ready": 2}]}]}`)

	var out strings.Builder
	if err := run(dir, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"first scale-up decision", "t=1s", "t=2s", // real decided at 1, sim at 2
		"900ms @ 1s", "800ms @ 1s",
		"p99 back under SLO for good",
		"16.67%", // 1 failed of 6 real requests
		"25.00%", // 1.5 failed of 6 simulated
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
