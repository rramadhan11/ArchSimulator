# ArchSim

Architecture simulator for capacity planning. Draw a system on a canvas (load balancers,
services, Kubernetes deployments and node pools, VMs, databases, caches, queues), set a
workload, and see whether it holds: latency percentiles, max TPS and what limits it, where
the bottleneck is, and how autoscaling behaves when traffic spikes.

Product & technical plan: [PLAN.md](PLAN.md) (section 0 lists the v1.1 decisions).

## Features

- **Canvas editor**: drag components from the palette, nest them (Service → Deployment →
  Node Pool), connect calls, and edit every parameter in a form generated from the catalog.
  Each value shows its default and where it comes from (vendor docs, benchmark, estimate,
  or yours).
- **Quick check**: re-runs on every edit and reports:
  - a verdict against the SLO
  - p50/p95/p99 latency and max TPS with the limiting component
  - per-hop latency and a utilization heatmap on the canvas
  - issues and the assumptions behind the numbers, with a confidence score
- **Time simulation**: steps the design once per second through a load pattern (constant,
  spike, ramp, step or diurnal) and models:
  - HPA and VM autoscaler control loops
  - pod startup, Pending pods, node provisioning and cluster-autoscaler scale-down
  - backlogs and timeouts when demand exceeds capacity
  - retries (backoff, retry budget) and circuit breakers, so retry storms show up
- **Failure injection**: lose nodes, pods or VMs, a whole zone, or a dependency (failing
  fast or hanging); add latency to a component or edge; fail a database over.
- **Probes and memory**: HTTP readiness and liveness probes that fail under a backlog (pods
  leave the Service or restart with back-off), and memory limits that OOMKill a pod whose
  backlog grows too large.
- **Cost**: on-demand monthly cost of node pools, VM groups and databases at the target load,
  and the cost of a simulated window as autoscaling adds and removes instances. Current rates
  are AWS Jakarta (ap-southeast-3) prices; every rate can be overridden per design.
- **Compare**: pin a baseline (or any saved version) and see the quick check and timeline of later
  edits next to it.
- **Saved designs with version history** on the server, a template gallery, a printable report
  (save as PDF from the browser) and a methodology page explaining every formula and limit.
- **Local-first designs**: autosave in the browser, JSON import/export (same format as the
  CLI), and share links that carry the compressed design in the URL hash.
- **Single-user login** in front of everything, so a public deployment is not open to
  anyone who finds the URL.

Not modeled yet: node memory pressure and eviction (container memory limits and OOM kills
are modeled), callers' threads held by a hung dependency, and retries in the quick check
(the time simulation models them, along with readiness and liveness probes). The calibration lab ([lab/](lab/README.md), run on
2026-11-02) now matches the HPA's scale-out within one sync period, the errors of an
overload, and the low-load latency of a pod under a fractional CPU limit (CFS quota);
throttling near the limit is not validated yet.

## How it works

The engine is pure Go with no IO.

| Mode | Function | Method |
|---|---|---|
| Quick check | `sim.Run` | Steady-state queueing models (Erlang C, M/M/c with a Kingman correction, USL), Little's Law, and seeded Monte Carlo sampling for percentiles. Max TPS comes from a search over load. |
| Time simulation | `sim.Simulate` | 1 s ticks with fluid backlogs. Runs the same autoscaling algorithms as Kubernetes and cloud autoscalers. Failed attempts come back as retries in later ticks; circuit breakers and injected failures change what each tick sees. At steady state it agrees with the quick check (golden tests). |

The engine is compiled to WebAssembly and runs in a Web Worker, so simulations happen in
the browser, and the Go server only serves the app and handles login.

## Layout

| Path | What |
|---|---|
| `engine/` | Go simulation engine: `model`, `catalog` (embedded YAML), `sim`, CLI in `cmd/archsim`, WASM entry in `cmd/wasm` |
| `engine/sim/` | Quick check (`run.go`, `evaluate.go`, `montecarlo.go`), time simulation (`timeline.go`), retries and circuit breakers (`resilience.go`), failure injection (`faults.go`) |
| `engine/catalog/data/` | Component catalog: every default carries a source |
| `engine/examples/` | Example designs: test fixtures and the web app's templates |
| `apps/web/` | React + React Flow editor, ECharts charts, zustand store |
| `apps/api/` | Go server: single-user login, design storage with versions (`/api/designs`), CSP, gzip, in front of the built web app |
| `deploy/` | Docker Compose deployment behind an existing Caddy, deploy/password/backup scripts ([deploy/README.md](deploy/README.md)) |
| `lab/` | Calibration lab: a reference service, HPA and k6 spike in kind, compared with ArchSim's simulation ([lab/README.md](lab/README.md)) |

## Getting started

Requires Go 1.23+ and Node 20+.

```sh
make test        # Go tests (engine, API) + web unit tests
make dev         # Vite dev server with hot reload, no login (local only)
make build       # engine.wasm, web app (dist/ + .gz copies), bin/archsim-api, bin/archsim
```

## Engine speed

The browser runs the engine as WebAssembly in a Web Worker. Median time per call on the
bundled templates (Apple M2 Pro, 5 November 2026; native from
`go test ./engine/sim -run '^$' -bench . -benchtime 5x`):

| | Quick check | Time simulation |
|---|---|---|
| Native Go | 15–30 ms | 130–260 ms |
| Chrome 157 (WASM) | 50–90 ms | 0.45–0.75 s |
| Node 23 (WASM) | 90–130 ms | 0.7–1.1 s |

The first call after loading is slower (up to ~1 s) while V8 optimizes the WASM code. In the
app, the K8s + Kong template shows a quick check in 86 ms and a time simulation in 0.6 s
(0.7 s from click to charts). The WASM file is 5.1 MB (1.4 MB gzip).

Measure without DevTools attached: an open DevTools/CDP session (Puppeteer, `--remote-debugging-port`
with a client) slows the page 10–100×, with charts taking seconds per render.

## Engine CLI

```sh
go run ./engine/cmd/archsim run engine/examples/k8s-kong-microservices.json
go run ./engine/cmd/archsim run --tps 8000 --json engine/examples/rest-postgres.json
go run ./engine/cmd/archsim simulate engine/examples/k8s-spike.json   # needs workload.timeline
go run ./engine/cmd/archsim simulate engine/examples/retry-storm.json # failure + retries
go run ./engine/cmd/archsim catalog
```

## Deploying the server (single-user auth)

```sh
cp .env.example .env
go run ./apps/api hash-password          # paste the hash into ARCHSIM_PASSWORD_HASH
openssl rand -hex 32                     # paste into ARCHSIM_SESSION_SECRET
make build && make run-api
```

- Everything except `/login` and `/healthz` requires the session cookie, including static
  files and `engine.wasm`.
- Failed logins are limited to 5 per IP per 15 minutes.
- Behind a reverse proxy, set `ARCHSIM_TRUST_PROXY=true` and keep
  `ARCHSIM_COOKIE_SECURE=true` (HTTPS).
- The app is served with a strict Content-Security-Policy: same-origin scripts plus
  `wasm-unsafe-eval`.
- Clients that accept gzip get precompressed `.gz` files.

See `.env.example` for all settings.

With Docker: the root `Dockerfile` builds everything (engine WASM, web app, server) into a
distroless image; [deploy/README.md](deploy/README.md) runs it on a VPS behind Caddy.

## Designs

- **Save** (Ctrl/Cmd+S) stores the design on the server as a new version; **Designs** lists saved
  designs, their version history (open or compare any version) and the templates. Versions are JSON
  files under `ARCHSIM_DATA_DIR` (default `data/`, one directory per design): back up that
  directory. Saving content identical to the latest version does not create a new one.
- The working copy also autosaves in the browser (localStorage), so a reload keeps unsaved edits.
- **Export** downloads the design JSON, the same format the CLI reads. Canvas positions
  live under `ui`, which the engine ignores.
- **Share link** puts the whole design, compressed, in the URL hash. Nothing is stored on
  the server, and opening the link still requires signing in.
