# Calibration lab

Checks ArchSim's time simulation against a real system (PLAN §9.3, the Phase 2
exit criterion: HPA behavior must match an experiment qualitatively). A tiny
service runs in a local Kubernetes cluster ([kind](https://kind.sigs.k8s.io/)),
[k6](https://k6.io/) sends it a traffic spike, and the result is compared second
by second with ArchSim's simulation of the same design.

**Run on 2026-11-02** on a 2 vCPU / 8 GB VPS (Ubuntu 24.04, kind v0.24.0,
Kubernetes v1.31). Results are under [Results](#results-2026-11-02).

## What runs

| Part | File | Size |
|---|---|---|
| Reference service: every request burns exactly 4 ms of CPU (measured as thread CPU time, so CPU throttling does not change the cost) | `cmd/work`, `Dockerfile` | 1–4 pods × 0.25 CPU, 64 MiB |
| Deployment + HPA (min 1, max 4, target 70%, default scaling behavior) and a NodePort Service | `k8s/app.yaml` | |
| One-node kind cluster + metrics-server | `k8s/kind.yaml` | ~1–1.5 GiB |
| Load: 30 req/s → spike to 120 req/s at t=60 s (30 s ramp, 240 s hold) → back to 30, 2 s client timeout | `k6/spike.js` | ≤ 300 virtual users |
| The same system in ArchSim | `design.json` | |
| Comparison of the two | `cmd/analyze` | |

Everything runs inside Docker, so Docker's CPU and memory limit caps the lab.
It needs ~2 GiB of memory and 2 CPU: Kubernetes' own pods request ~1 CPU, so the
node has room for the service's 4 pods × 0.25 CPU (the prediction peaks at 3;
the cap of 4 keeps the 5th from going Pending on a 2-CPU machine). Real use
peaks around 1–1.2 CPU. With fewer than 4 CPU the service shares cores with
kind and k6, so latency is noisier; HPA timing, the main thing checked, is set
by controller timers and is not affected. One run takes about 8 minutes plus
setup.

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

## Results (2026-11-02)

**Run 1: HTTP readiness probe (`/healthz`, 1 s timeout).**
- Under the spike the probe timed out: it waited behind the requests sharing the
  pod's 0.25 CPU.
- The only pod went NotReady and left the Service, so 71% of requests failed
  over the run.
- The HPA reported "did not receive metrics for targeted pods (pods might be
  unready)" and first scaled at t=248 s; the simulation scaled at t=75 s.
- ArchSim now models HTTP readiness probes (`readinessProbe.kind: http`).
  The lab design with run 1's probe flaps in the simulation (out of the
  Service for 1–2 s every ~4 s) but recovers once the HPA adds pods: 3.3%
  errors against the real 71%. Two real effects explain the gap, and both are
  documented rather than fitted:
  - The lab's pod was under 5 minutes old, so the HPA got no CPU metric while
    the pod was not Ready. The simulation starts from a long-running steady
    state.
  - kube-proxy keeps sending keep-alive traffic to a not-Ready pod, so the pod
    stayed saturated. ArchSim stops all traffic, as an L7 proxy does.

**Run 2: TCP readiness probe** (answered by the kernel, as `k8s/app.yaml` now has):

| | real | ArchSim |
|---|---|---|
| first scale-up decision | t=102 s | t=75 s |
| peak ready pods | 3 at t=149 s | 3 at t=96 s |
| p99 back under the SLO | t=323 s | t=96 s |
| errors over the run | 20.3% | 0.13% |

- **HPA: matches qualitatively** (the PLAN §9.3 bar): the same 1 → 2 → 3
  sequence and the same peak pod count. The real decisions came later: 1 → 2
  at t=102 s (simulated 75 s) and 2 → 3 at t=148 s (simulated 90 s).
- **Overload: ArchSim was optimistic.** In the real run nearly every request
  timed out from t=80 s to t=150 s; the simulation recovered by t=96 s. The cause
  turned out to be the HPA, not the queue model:
  - The real HPA reads CPU from metrics-server: a 15 s average, already
    ~15 s old.
  - A pod that just became ready has no metric covering its ready time yet,
    so on a scale-up it counts as using 0% (Kubernetes' replica calculator).
  - Two pods served 125 req/s against 120 req/s, so the backlog did not drain
    and requests kept timing out until the third pod came.

**After modeling both** (`hpa.metricsWindowS` 15 s from the metrics-server
manifest, `hpa.metricsDelayS` 15 s fitted here), against the same run:

| | real | ArchSim before | ArchSim after |
|---|---|---|---|
| 1 → 2 decision | t=102 s | t=75 s | t=90 s |
| 2 → 3 decision | t=148 s | t=90 s | t=135 s |
| peak ready pods | 3 at t=149 s | 3 at t=96 s | 3 at t=141 s |
| errors over the run | 20.3% | 0.13% | 19.6% |

The remaining offset is within one 15 s sync period (the real HPA's sync phase
is unknown). Processor sharing (Go serving all in-flight requests at once) is
not needed to explain the errors: the first-in first-out backlog with
abandoned requests still served gets the same result. `TestLabDesignHPATiming`
pins these timings.
- **Latency under the 0.25 CPU limit is off in both directions** (CFS quota:
  the pod runs at full core speed until it has used 25 ms of each 100 ms period,
  then waits; ArchSim models a core 4× slower):

  | per pod | real p50 / p99 | ArchSim p50 / p99 |
  |---|---|---|
  | 16% (30 req/s, 3 pods) | 4.4 / 8.8 ms | 16 / 17 ms |
  | 48% (30 req/s, 1 pod) | 4.4 / 8.3 ms | 16 / 75 ms |
  | 64% (120 req/s, 3 pods) | 31 / 134 ms | 16 / 43 ms |

- The HPA asked for a 4th pod at t=193 s, most likely because the pods were
  still at full CPU draining the backlog (p50 was still ~300 ms then). An
  earlier version of this README guessed extra CPU per request instead; the
  timing points to the backlog. The 4th pod stayed Pending: Kubernetes' own pods
  requested 1.05 CPU, so the 2-CPU node had room for 3 service pods.
- k6 dropped ~190 iterations (out of virtual users) at the peak, so the real
  offered load was slightly below the simulated one.

**After modeling CFS quota** (`engine/sim/cfs.go`; same run):

| per pod | real p50 / p99 | ArchSim p50 / p99 |
|---|---|---|
| 16% (3 pods) | 4.4 / 8.7 ms | 4.0 / 7.0 ms |
| 48% (1 pod) | 4.4 / 8.3 ms | 4.0 / 19.8 ms |
| 64% (3 pods) | 31 / 135 ms | 4.0 / 61 ms |

- **Low load matches.** The remaining 0.4 ms is HTTP and network overhead.
- **The 48% p99 differs because of the arrivals.** k6 sends requests evenly
  spaced, while ArchSim assumes random (Poisson) arrivals, as production
  traffic has. A one-pod simulation with k6's spacing gives p99 4 ms, with
  Poisson 18 ms.
- **The 64% point does not validate the throttling tail.** Its spread (p25
  10 ms, p50 31 ms, p90 80 ms) is not the shape quota throttling gives (most
  requests at 4 ms plus a tail of up to 75 ms). The likely cause: on the 2-CPU
  machine, the pods also waited for CPU behind the Kubernetes control plane and
  k6. Validating it needs a machine with spare CPU.

Next calibration steps:
1. Validate CFS throttling near the limit on a machine with spare CPU (≥ 4).
2. Measure CPU per request (and set `cpuMsPerReq` from it).
3. A smaller spike that peaks at 3 pods fits this 2 vCPU VPS; the full run needs
   a machine with ≥ 4 CPU.

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
