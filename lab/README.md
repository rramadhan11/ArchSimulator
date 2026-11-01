# Calibration lab

Checks ArchSim's time simulation against a real system (PLAN §9.3, the Phase 2
exit criterion: HPA behavior must match an experiment qualitatively). A tiny
service runs in a local Kubernetes cluster ([kind](https://kind.sigs.k8s.io/)),
[k6](https://k6.io/) sends it a traffic spike, and the result is compared second
by second with ArchSim's simulation of the same design.

**Not run yet.** The files are ready; nothing has been executed.

## What runs

| Part | File | Size |
|---|---|---|
| Reference service: every request burns exactly 4 ms of CPU (measured as thread CPU time, so CPU throttling does not change the cost) | `cmd/work`, `Dockerfile` | 1–6 pods × 0.25 CPU, 64 MiB |
| Deployment + HPA (min 1, max 6, target 70%, default scaling behavior) and a NodePort Service | `k8s/app.yaml` | |
| One-node kind cluster + metrics-server | `k8s/kind.yaml` | ~1–1.5 GiB |
| Load: 30 req/s → spike to 120 req/s at t=60 s (30 s ramp, 240 s hold) → back to 30, 2 s client timeout | `k6/spike.js` | ≤ 300 virtual users |
| The same system in ArchSim | `design.json` | |
| Comparison of the two | `cmd/analyze` | |

Everything runs inside Docker, so Docker's CPU and memory limit caps the lab
(Docker Desktop on the dev laptop: 4 CPU / ~3.8 GiB). The service needs at most
1.5 CPU; one run takes about 8 minutes plus setup.

## Run

Needs Docker, kubectl and Go. kind and k6 are not installed: they run from
pinned versions (`go run sigs.k8s.io/kind@v0.24.0`, image `grafana/k6:0.54.0`).

```sh
make lab          # = lab/run.sh: asks before starting, writes lab/results/
make lab-clean    # deletes the kind cluster
```

The report (`lab/results/report.txt`) shows, per 10 s: request rate, p99,
error rate and ready/desired pods, real next to simulated, then milestones:
first scale-up decision, peak pods, peak p99, when p99 is back under the SLO,
and total errors. ArchSim's prediction for this design:

- HPA scales 1 → 2 at t=75 s and 2 → 3 at t=90 s
- backlog from t=71 s to t=86 s; p99 hits the 2 s timeout, errors during the ramp
- 3 pods for the rest of the hold; no scale-down within the run (300 s stabilization)

## What to look for

Known modeling gaps this experiment should size up:

- **Metrics lag.** metrics-server scrapes every 15 s and the HPA acts on that,
  while ArchSim's HPA sees CPU averaged over its own 15 s sync period. Expect the
  real first scale-up later, by up to ~15–30 s.
- **New pods' CPU.** The HPA ignores a pod's CPU right after it starts
  (`--horizontal-pod-autoscaler-cpu-initialization-period`, 5 min, and the 30 s
  initial readiness delay). ArchSim does not model this.
- **Pod startup.** `design.json` assumes `podStartupS: 5`. Read the real value
  off the run (desired → ready) and put it in the design.
- **CPU throttling.** A 0.25 CPU limit is enforced in 100 ms CFS periods (25 ms
  of CPU, then a pause), and Go shares the CPU between in-flight requests
  instead of serving them in order. ArchSim models a queue at 0.25 speed, so
  tail latency at moderate load may differ even when throughput matches.

The target from PLAN §9.3 for max throughput is ±25%; for HPA timing the bar
is qualitative (same sequence of events, similar timing).

Change `k8s/app.yaml`, `k6/spike.js` and `design.json` together: they describe
the same experiment.
