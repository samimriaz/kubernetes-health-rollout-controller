# Kubernetes Health Rollout Controller

Health-gated progressive canary rollouts with Prometheus monitoring and
automatic rollback. Simulated GPU health signals are a later increment.

A Kubernetes operator that automates canary rollouts gated on live health signals, with automatic rollback on failure. MVP is a real inference workload with stable/canary rollout and app-health-triggered rollback; GPU telemetry and chaos injection are later increments, not dependencies. Fully local, reproducible with `kind`, no production access required.

## 1. Motivation

Modern infra teams (especially AI/ML platform teams) need deployments that can detect unhealthy rollouts and roll back automatically before impact spreads. This project demonstrates that pattern end-to-end using a real inference workload, not a mocked-up service, so the health signals being gated on are genuine. Scope is deliberately staged: prove the core rollback loop first, then layer on GPU signals and chaos-induced failure as convincing enhancements.

## 2. Goals

- Build a custom Kubernetes controller that watches a rollout and gates progression on health signals from Prometheus.
- Serve a real small-model inference workload so app health signals reflect actual behavior under real load, not scripted toggles.
- Get a reliable stable/canary rollback loop working end-to-end before adding GPU or chaos features.
- Visualize the loop (deploy → detect → rollback) in Grafana and in a recorded terminal demo.
- Produce a portfolio-ready GitHub repo: one-command demo, clear README, recorded GIF/asciinema.

## 3. Non-Goals

- Not a production-hardened controller (no multi-tenancy, RBAC hardening, HA leader election beyond basics).
- Not a large-scale model — inference service uses MobileNet/ONNX, chosen for being lightweight and deterministic.
- Not integrating with a real cloud provider.
- Not claiming real GPU telemetry when running without GPU hardware — simulated GPU signals are named and documented as simulated, never as real DCGM metrics.

## 4. MVP Scope (Milestones 1–4) vs. Later Increments

**MVP — build and prove this first:**
1. Real inference service (MobileNet/ONNX) with genuine Prometheus metrics
2. Prometheus + Grafana stack (installed before validating load metrics)
3. Load generator producing real, visible traffic
4. Controller with a defined stable/canary rollout and an app-health-triggered rollback

**Later increments — add only once the MVP rollback loop is reliable:**
- GPU telemetry (simulated, clearly labeled)
- Chaos Mesh failure injection
- Dashboard polish / annotations

Rationale: a working rollback loop is the actual portfolio proof point. GPU and chaos features are convincing enhancements on top of that proof, not substitutes for it — and building them first risks a flashy-looking repo that doesn't reliably do the one thing it claims to do.

## 5. Architecture Overview

```
                ┌────────────────────────────────────┐
                │           kind cluster              │
                │                                      │
                │  ns: workload                        │
  demo.sh ─────▶│    inference-service (stable + canary)│
                │    load-generator                    │
                │                                      │
                │  ns: monitoring                      │
                │    Prometheus, Grafana               │
                │                                      │
                │  ns: rollout-system                  │
                │    rollout-controller                │◀── watches Rollout CRD
                │                                      │
                │  ns: chaos-testing  (later increment)│
                │    Chaos Mesh                        │
                └────────────────────────────────────┘
```

Each concern gets its own namespace (workload, monitoring, rollout-system, chaos-testing) rather than one shared namespace — this is both good practice and a practical necessity: the existing `ads-team-quota.yaml` five-pod quota cannot fit the complete stack in a single namespace.

**MVP flow:** load-generator drives real requests against the shared Service in front of stable + canary Deployments → controller shifts the stable:canary replica ratio in steps → on each step, queries Prometheus (via configurable PromQL, filtered by the `track` label) over a defined observation window for canary error rate and latency → if thresholds are breached across a required number of consecutive failing samples, controller halts and rolls back by returning canary replicas to zero → Grafana shows the sequence.

## 6. Components

### 6.1 `/controller` — Rollout Controller
- Language: Go, built with `kubebuilder` or `operator-sdk`.
- Watches a custom `HealthGatedRollout` CRD that manages **stable and canary Deployments sitting behind a single shared Service** (see 6.2 for the traffic-routing model) — this is what lets the controller shift traffic and roll back cleanly.
- Progresses canary in defined steps by adjusting the **stable:canary replica ratio** behind the shared Service (e.g., 9:1 → 1:1 → 0:1, approximating 10% → 50% → 100% traffic).
- **Rollout behavior is precisely specified, not left implicit:**
  - **Observation window:** e.g., evaluate metrics over a rolling 60s window per step.
  - **Minimum sample count:** don't evaluate a step's health until at least N requests have been observed in the window — avoids judging on too little data.
  - **Thresholds:** explicit numeric thresholds for error rate and p95/p99 latency, defined per step.
  - **Consecutive-failure requirement / cooldown:** require the threshold to be breached for M consecutive evaluation cycles before triggering rollback, and apply a cooldown after any rollback before the controller will re-attempt promotion — avoids noisy single-sample metrics triggering a flapping rollback.
  - **Prometheus unavailable / no-data behavior:** explicitly defined — e.g., treat a failed/empty Prometheus query as "hold at current step, do not promote, do not roll back" rather than either failing open (promote blindly) or failing closed (roll back on missing data). This distinction is a good interview talking point.
- On confirmed breach: pauses rollout, scales canary replicas back to zero (rollback, per the shared-Service model in 6.2), emits a Kubernetes Event and a Prometheus-visible marker.
- **Signal-provider interface:** the controller consumes health signals through a small internal interface (e.g., `SignalProvider.Evaluate(step) (healthy bool, reason string)`), backed by a single implementation that queries Prometheus using **configurable PromQL** (query strings supplied via CRD/config, not hardcoded). This is also how the later GPU increment plugs in (see 6.4): DCGM Exporter, like the simulated exporter, only ever exposes Prometheus metrics — it is not itself a `SignalProvider` implementation. Swapping simulated GPU signals for real DCGM later means changing the PromQL query configuration, not writing new controller logic.
- Reconcile loop and rollout-decision logic are the core pieces to showcase in interviews/READMEs.

### 6.2 `/inference-service` — Real Inference Workload
- **MobileNet via ONNX Runtime**, behind a small FastAPI (or Go) server. Chosen specifically over an LLM or Whisper workload because it's lighter, faster to iterate on, and far more deterministic — important when you're trying to reason precisely about thresholds and observation windows.

**Traffic-routing model (resolve first — this drives the CRD design, metrics labels, controller behavior, and demo architecture):** separate stable/canary Services cannot provide weighted traffic splitting (10/50/100) on their own — a Service just load-balances evenly across whatever pods match its selector. Two options:
  - **Shared Service, approximate weighting (MVP choice):** one Service selects both stable and canary pods via a common label (e.g., `app: inference-service`); the stable:canary **replica count ratio** approximates the traffic split (e.g., 9 stable + 1 canary ≈ 10% canary traffic). Simple, no extra infrastructure, good enough for a portfolio demo.
  - **Gateway API / service mesh, exact weighting:** precise percentage-based traffic splitting, but adds real infrastructure (Gateway API implementation or a mesh like Istio/Linkerd) — worth calling out as a stretch goal, not MVP scope.
  - **MVP uses the shared-Service approach.**
- Stable and canary run as **separate Deployments** (distinct pod template labels, e.g., `track: stable` / `track: canary`) but sit behind the one shared Service described above.
- **Metrics must be labeled to be evaluable per-track:** request-count and latency-histogram metrics expose a `track` label (`stable`|`canary`) — and ideally `pod` or `version` too — so the controller's PromQL queries can isolate canary health independently from stable (e.g., `rate(http_requests_total{track="canary", status="5xx"}[60s])`). Without this label, the controller has no way to tell which track a given metric sample came from.
- Exposes real Prometheus metrics: request count, error count, latency histogram — derived from actual inference calls, all carrying the `track` label above.
- CPU-only by default; runs identically with or without GPU hardware, since MobileNet inference on CPU is fast enough for demo purposes.
- Hardened over time (not required for MVP, but on the roadmap): pinned image versions, readiness/liveness probes, a defined security context, and the Prometheus instrumentation above.

**How the canary becomes unhealthy (important — load alone doesn't create failure):** a rollout is only risky if the new version actually behaves worse than the old one. Sending load at two identical healthy versions just produces two sets of healthy metrics. So the canary must be deliberately, controllably worse than stable. For the primary demo, use:
  - **Resource-starved canary (MVP mechanism):** canary Deployment sets a CPU/memory limit deliberately too low for MobileNet inference under load, so it starts throwing errors or slowing down once real traffic ramps up. This is a genuine health signal — the canary really is failing under real resource pressure, not pretending to.
  - Avoid an injected-fault flag (e.g., `INJECT_FAULT=true`) as the *primary* demo mechanism — it works, but it sits awkwardly against the project's core goal of genuine health signals over scripted toggles. It's fine as a documented alternative/fallback for fast iteration while developing thresholds, just not the headline mechanism in the README/demo.
  - **Broken dependency/config** (e.g., wrong model file path) remains a documented "more realistic, less deterministic" alternative, not MVP default.
- The load-generator (6.3) is what turns this engineered flaw into real, measurable degraded metrics (rising error rate, rising p95 latency) for the controller to detect — load and "the canary is resource-starved" are two separate, necessary ingredients, not one mechanism.

### 6.3 `/load-generator` — Real Traffic Driver
- A dependency-free Python process creates a real PNG in memory and uploads it
  to `POST /predict` as multipart form data. No sample image or volume mount is
  required.
- `TARGET_URL`, `REQUESTS_PER_SECOND`, and `REQUEST_TIMEOUT_SECONDS` make the
  destination, traffic rate, and timeout configurable from the Deployment.
- The initial Deployment sends one request per second. Raising
  `REQUESTS_PER_SECOND` later will test the controller's response to genuine
  degradation under load.

Build, load, and deploy it locally:

```powershell
docker build -t load-generator:v0.1.0 .\load-generator
kind load docker-image load-generator:v0.1.0 --name health-rollout
kubectl apply -f .\kubernetes\workload\load-generator-deployment.yaml
kubectl rollout status deployment/load-generator -n workload
kubectl logs deployment/load-generator -n workload --tail=10
```

Useful Grafana/Prometheus queries:

```promql
# Requests per second
sum(rate(inference_http_requests_total{track="stable",path="/predict"}[1m]))

# Fraction of requests returning HTTP 5xx; show zero before any 5xx exists
(sum(rate(inference_http_requests_total{track="stable",path="/predict",status=~"5.."}[5m])) or vector(0))
/ clamp_min(sum(rate(inference_http_requests_total{track="stable",path="/predict"}[5m])), 0.001)

# 95th-percentile inference latency in seconds
histogram_quantile(0.95,
  sum by (le) (
    rate(inference_http_request_duration_seconds_bucket{track="stable",path="/predict"}[5m])
  )
)
```

### 6.3.1 Run the Health-Gated Rollout

Build and load the controller image, install its CRD and controller, then create
the sample rollout:

```powershell
docker build -t health-rollout-controller:v0.1.0 .\controller
kind load docker-image health-rollout-controller:v0.1.0 --name health-rollout
kubectl apply -k .\controller\config\default
kubectl rollout status deployment/rollout-controller-manager -n rollout-system
kubectl apply -f .\controller\config\samples\rollout_v1alpha1_healthgatedrollout.yaml
```

Watch the decision and verify replica restoration after rollback:

```powershell
kubectl get healthgatedrollout inference-rollout -n workload -w
kubectl get deployments inference-stable inference-canary -n workload
kubectl get events -n workload --field-selector involvedObject.name=inference-rollout
```

The sample holds when Prometheus has no data, waits for at least five canary
requests, and requires two unhealthy evaluations 15 seconds apart. On rollback,
stable returns to one replica and canary returns to zero for a five-minute
cooldown.

### 6.4 `/gpu-metrics` — Simulated GPU Telemetry (later increment, not MVP)
- Named and exposed explicitly as **`simulated_gpu_*`** metrics (e.g., `simulated_gpu_ecc_errors_total`, `simulated_gpu_util_percent`) — never published under real `DCGM_FI_DEV_*` names, since doing so on a CPU-only signal would be misleading to anyone reading the metrics or the code.
- Both the simulated exporter and a real DCGM Exporter are just Prometheus metric sources — neither is itself a `SignalProvider`; the controller's one `SignalProvider` implementation queries Prometheus via configurable PromQL (see 6.1). Swapping simulated GPU signals for real DCGM later means pointing the query configuration at different metric names, not writing new controller code — the interface boundary is what makes the "simulated vs. real" distinction honest rather than hand-wavy.
- Real GPU access through `kind` (especially on Windows/WSL2) is expected to be one of the hardest parts of this project environment-wise. The CPU/simulated path is the **guaranteed, documented local demo**; real-GPU mode is documented as an explicitly optional, separate environment path — not something the main demo depends on.

### 6.5 `/chaos` — Failure Injection (later increment, sequenced after MVP rollback works)
- **Sequencing matters:** first prove rollback works using ordinary load/resource pressure from the load-generator alone (6.3). Only after that's reliable, layer in Chaos Mesh. Introducing chaos tooling before the basic loop is proven mixes two sources of failure (chaos tool misconfiguration vs. controller logic bugs) and makes debugging much harder.
- Chaos Mesh experiment manifests (pod kill, network latency, CPU stress) to produce failure conditions beyond what the load-generator alone can create.
- Deployed in its own `chaos-testing` namespace, separate from the workload and monitoring stacks.

### 6.6 `/dashboards` — Grafana Dashboards (polish increment, after MVP)
- JSON dashboard definitions checked into git (reviewable without running the project).
- Panels: rollout progress/step, real request latency & error rate, rollback event annotations; simulated GPU panels added once 6.4 lands.
- Deployed via `kube-prometheus-stack` Helm chart, in its own `monitoring` namespace.

### 6.7 `/scripts` — One-Command Demo
- `demo.sh` (MVP version):
  1. Spin up `kind` cluster
  2. Create namespaces: `workload`, `monitoring`, `rollout-system`
  3. Install `kube-prometheus-stack` into `monitoring`
  4. Deploy stable + canary `inference-service` (behind the shared Service, per 6.2), controller + CRDs into their namespaces
  5. Start the load-generator against the shared Service
  6. Start a rollout — canary is running resource-starved (per 6.2) from the start, so ramping load surfaces real degraded metrics
  7. Show controller detecting the issue via real metrics and rolling back, live in terminal output
  8. Print a link/hint to open the Grafana dashboard
- Extended later with a `chaos-testing` namespace and a `--chaos` flag once 6.5 lands.

**Environment requirements (document explicitly in README, especially for Windows):**
- Windows: WSL2 (Ubuntu recommended) or Git Bash, since `demo.sh` is a bash script
- Docker Desktop (with WSL2 integration enabled, if on Windows)
- `kind`, `kubectl`, `helm` — versions pinned in README
- No GPU drivers/toolkit required for the default CPU/simulated path

## 7. Milestones

| # | Milestone | Deliverable | Phase |
|---|-----------|-------------|-------|
| 1 | Local cluster + real inference service running | `kind` cluster, namespaces created, stable inference-service deployed, real requests succeed | MVP |
| 2 | Prometheus + Grafana stack | Prometheus/Grafana installed and scraping the inference service, base dashboard, deployed in `monitoring` namespace | MVP |
| 3 | Load generator producing real, visible traffic | Configurable request rate; latency/error metrics from real calls visible in Grafana (Prometheus already installed per milestone 2) | MVP |
| 4 | Controller v1 — stable/canary rollout with app-health rollback | Reconcile loop with defined observation window, sample minimums, thresholds, consecutive-failure/cooldown logic, and explicit no-data behavior; rolls back reliably on real error rate/latency breach | MVP |
| 5 | Simulated GPU metrics wired in | `simulated_gpu_*` exporter implementing the `SignalProvider` interface; controller thresholds extended | Later increment |
| 6 | Chaos experiments | Chaos Mesh introduced only after milestone 4 is proven reliable with ordinary load/resource pressure | Later increment |
| 7 | Dashboards + annotations | Grafana panels show rollout + rollback events clearly, including simulated GPU panels | Later increment |
| 8 | Demo polish | `demo.sh` one-command run, asciinema/GIF recording, README | Later increment |

**Best first portfolio checkpoint:** milestones 1–4 complete — a real inference service with metrics, a stable/canary rollout, and an app-health-triggered rollback that works reliably. That alone is a legitimate, demo-able project. GPU and chaos features are enhancements on top of it, not blockers to shipping it.

## 8. README / Portfolio Presentation Plan

- Short problem statement (why health-gated rollouts matter).
- Architecture diagram (from Section 5), with a clear note on what's MVP vs. later increments.
- **Recorded demo GIF or asciinema** near the top, of the MVP rollback loop — this is the highest-leverage asset for reviewers who won't run the code.
- "How to run it" — `git clone && ./scripts/demo.sh`.
- "Design decisions" section explaining the rollout-decision logic (observation windows, thresholds, no-data handling) — good source material for interview talking points.
- Explicit, upfront note that GPU metrics are simulated (`simulated_gpu_*`) unless run in the optional real-GPU environment — no ambiguity about what's real vs. simulated anywhere in the repo.

## 9. Stretch Goals (optional, well beyond MVP)

- Support multiple rollback strategies (immediate vs. gradual) and benchmark recovery time.
- Add a PromQL-based Alertmanager rule feeding directly into the controller's decision loop.
- Multi-cluster simulation (two `kind` clusters) to show cross-cluster rollout safety.
- Validate the controller's PromQL queries against real DCGM Exporter metric names if real GPU hardware access becomes available (see project notes — parked for now).

## 10. Tech Stack Summary

- **Cluster:** kind (Kubernetes-in-Docker), namespaced: `workload`, `monitoring`, `rollout-system`, `chaos-testing`
- **Controller:** Go, kubebuilder/operator-sdk, client-go
- **Inference workload:** FastAPI/Go server + MobileNet (ONNX Runtime), CPU-only by default
- **Load generation:** k6, hey, or a small custom script
- **Metrics:** Prometheus, kube-prometheus-stack (Helm), Grafana
- **Simulated GPU telemetry:** `simulated_gpu_*`-named exporter implementing a shared `SignalProvider` interface, swappable for real DCGM Exporter later
- **Chaos:** Chaos Mesh (introduced after MVP rollback is proven)
- **Demo automation:** bash scripts, asciinema for recording