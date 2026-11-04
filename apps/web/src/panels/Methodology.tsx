import type { ReactNode } from 'react'
import { Dialog } from '../ui/kit'

/** How the numbers are produced, and where they stop being trustworthy. Keep in sync with engine/sim. */
export function Methodology({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onClose={onClose} title="How ArchSim computes its numbers" wide>
      <div className="space-y-5 p-5 text-[12.5px] leading-relaxed">
        <p>
          Results are <b>estimates</b> from queueing models. Their accuracy depends on the parameters you enter, so every parameter shows its source, and the confidence score says how much of the result rests on rough
          defaults.
        </p>

        <Part title="Quick check (steady state)">
          <li>
            Each component becomes one or more <b>stations</b>: CPU cores of a service's replicas, proxy workers, database primary and replicas, cache threads, queue partitions. Load per station follows the call graph:
            calls per request × probability (or cache hit/miss ratio) × request-class mix.
          </li>
          <li>
            Utilization <Mono>ρ = λ / (c·μ)</Mono>. Waiting time uses <b>M/M/c (Erlang C)</b> with the <b>Kingman / Allen-Cunneen</b> correction for service-time variability:{' '}
            <Mono>Wq ≈ (1 + cs²)/2 · Wq(M/M/c)</Mono>.
          </li>
          <li>
            Multi-core replicas lose efficiency to contention via the <b>Universal Scalability Law</b> (σ, κ). CPU limits cap capacity; HPA targets are relative to CPU requests.
          </li>
          <li>
            A CPU limit below the pod&apos;s threads (e.g. 250m, 1500m) is enforced by Linux <b>CFS quota</b>: the pod runs at full core speed until it has used limit × 100 ms of CPU in the current
            100 ms period, then waits for the next. Capacity is the limit, but latency is not that of a slower core: fast at low load, with waits of up to a period near the limit. The wait distribution
            of such a pod comes from a small event simulation of one replica (its own queue, Poisson arrivals). Other stations share one queue across replicas (Erlang C), which is optimistic when a
            load balancer routes at random.
          </li>
          <li>
            Percentiles come from a <b>seeded Monte Carlo</b> pass (20,000 requests by default) that samples queue waits, service times, network round trips and fan-out (parallel calls take the slowest, sequential calls
            add up). The same design and seed always give the same numbers.
          </li>
          <li>
            In-flight requests, pooled connections and database connections follow <b>Little&apos;s law</b> (rate × time held).
          </li>
          <li>
            Autoscaled services get the replica count at which average CPU equals the target. Pods are packed onto nodes with <b>first-fit decreasing</b> using GKE&apos;s allocatable-CPU/memory formula; pods that do not
            fit stay Pending.
          </li>
          <li>
            <b>Verdict</b>: safe below the warning threshold (70%), warning up to the overload threshold (85%) or when p99 is within 20% of the SLO, overload above that, when queues are unstable, the p99 SLO or error
            budget is broken, or pods are Pending. A resource whose autoscaler can still add capacity is only judged against the overload threshold.
          </li>
          <li>
            <b>Max sustainable TPS</b>: exponential search then bisection (to 0.2%) for the highest load whose verdict is not overload.
          </li>
        </Part>

        <Part title="Time simulation (1-second steps)">
          <li>
            Offered load follows the pattern (constant, spike, ramp, step, diurnal) and flows through the stations in call order. A station serves at most its capacity per second; the rest becomes a <b>backlog</b>{' '}
            served first-in first-out. Backlog older than the queue timeout is dropped and counted as errors.
          </li>
          <li>
            Below saturation, latency uses the same M/M/c formulas as the quick check (the two modes agree in steady state; this is tested). Saturated stations add the wait behind their backlog, which grows or drains
            across the second.
          </li>
          <li>
            <b>Kubernetes HPA</b> runs every sync period (15 s) on the CPU pods actually used (a saturated pod cannot report more than its limit), read the way metrics-server delivers it: a 15 s average that is
            already about 15 s old. Pods without a metric yet (Pending, starting, or ready for less than a metric window) count as using 0% when the others ask to scale up, which holds scale-up back as in
            Kubernetes&apos; replica calculation. Then a 10% tolerance, scale-up of 100% or 4 pods per period, and a 300 s scale-down stabilization window. New pods become ready after the startup delay; pods that do not fit stay Pending until the <b>cluster autoscaler</b> adds nodes after the
            provisioning delay, and unneeded nodes are removed after 10 minutes. VM groups follow target tracking with boot time and scale-in cooldown.
          </li>
          <li>
            <b>Retries</b>: a failed attempt (error or timeout) is retried after the time it took to fail plus exponential backoff, in a later second, so a retry storm builds up the way it does in production. A retry
            budget caps retries at a share of first attempts. <b>Circuit breakers</b> open when the failure rate over their window passes the threshold, fail calls fast while open, and probe before closing.
          </li>
          <li>
            <b>Readiness probes</b> (Deployments with <Mono>readinessProbe.kind: http</Mono>): the probe request waits behind the pod&apos;s backlog, so it fails once that wait exceeds its timeout. After
            the failure threshold the pods leave the Service and new requests fail at once, while the pods finish their backlog; the probe then passes and traffic returns, so an overloaded Deployment
            flaps. A not-Ready pod younger than 5 minutes gives the HPA no CPU metric. TCP probes are answered by the kernel and never fail from load.
          </li>
          <li>
            <b>Failure injection</b>: lost nodes/VMs/pods (controllers replace what they manage after their usual delay), a zone outage, a dependency that errors or hangs, added latency, and database failover.
          </li>
        </Part>

        <Part title="Where parameters come from">
          <li>
            Every default in the catalog carries a source: <b>vendor docs</b> (with a link), <b>vendor benchmark</b> (ideal conditions), or <b>estimate</b> (our rough guess). Values you set count as{' '}
            <b>yours</b>.
          </li>
          <li>
            The <b>confidence score</b> averages source quality over each resource&apos;s critical parameters (yours 1.0, docs 0.9, benchmark 0.6, estimate 0.3), weighted by how loaded the resource is: an estimate on
            the bottleneck lowers it most. High ≥ 0.75, medium ≥ 0.5.
          </li>
          <li>The single most valuable input is a measured CPU time per request (cpuMsPerReq) for your services, from profiling or a load test.</li>
        </Part>

        <Part title="Not modeled (yet)">
          <li>Memory pressure: OOM kills, eviction, garbage-collection pauses beyond the latency variability parameter.</li>
          <li>Threads or connections a caller holds while waiting on a slow or hung dependency (so the caller does not saturate from waiting alone).</li>
          <li>
            Liveness probes (a failing one restarts the container and loses its work), and clients that keep connections open: kube-proxy only stops new connections to a not-Ready pod, so keep-alive
            traffic keeps reaching it. ArchSim stops all traffic, as an L7 proxy (Ingress, Envoy, Kong) does. All replicas of a Deployment pass or fail their probe together.
          </li>
          <li>Retries and circuit breakers in the quick check; they act in the time simulation only.</li>
          <li>Cost, cold caches warming up, noisy neighbours, and network failures between zones.</li>
        </Part>

        <Part title="How it is validated">
          <li>Golden tests compare the engine with closed-form results (M/M/1, M/M/c, M/D/1, Little&apos;s law), check both modes agree in steady state, and check the autoscaler timelines event by event.</li>
          <li>
            A calibration lab (a reference service with an HPA under a k6 spike in Kubernetes, in the repository&apos;s <Mono>lab/</Mono>) compares the time simulation with a real run. In the first run (November
            2026), before the HPA modeled metrics delay and pods without metrics, real scale-ups came 27–58 s after simulated ones and real errors were 20% against 0.13%. With both modeled, the decisions
            land within one sync period of the real ones and errors match (19.6% against 20.3%). With CFS quota modeled, p50 at low load matches (4.0 against 4.4 ms; a core 4× slower gave 16 ms). Throttling
            near the limit is not validated yet: on the lab&apos;s 2-CPU machine, the pods also waited for CPU behind Kubernetes and k6 (real p50 31 ms at 64% against 4 ms simulated).
          </li>
        </Part>
      </div>
    </Dialog>
  )
}

function Part({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h3 className="mb-1.5 text-[11px] font-semibold tracking-wide text-muted uppercase">{title}</h3>
      <ul className="list-disc space-y-1.5 pl-5">{children}</ul>
    </section>
  )
}

function Mono({ children }: { children: ReactNode }) {
  return <code className="font-mono text-[11.5px]">{children}</code>
}
