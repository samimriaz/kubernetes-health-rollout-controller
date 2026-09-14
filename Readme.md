# Kubernetes Health Rollout Controller

This project demonstrates a safer way to release a new application version on
Kubernetes. Instead of replacing every running instance at once, it keeps the
known-good version running and sends only part of the traffic to the new version.

The known-good version is called **stable**. The new version being tested is
called the **canary**. Prometheus measures the canary's request errors, response
time, and simulated GPU health. A custom Go controller checks those measurements
before allowing the release to continue. If the canary remains healthy, the
controller moves to the next rollout step. If it repeatedly exceeds a safety
limit, the controller stops the canary and returns all traffic to stable.

The project also includes Chaos Mesh experiments for testing failures and a
Grafana dashboard for viewing traffic, latency, GPU signals, and rollbacks. The
complete system runs locally in a `kind` cluster and does not require production
access or GPU hardware.

The release follows four clear stages:

```mermaid
flowchart LR
   Deploy[Deploy candidate] --> Evaluate[Evaluate health]
   Evaluate -->|Healthy| Promote[Promote release]
   Evaluate -->|Unhealthy| Rollback[Roll back to stable]
```

1. **Deploy:** Start the candidate release beside the stable application.
2. **Health evaluation:** Prometheus measures requests, latency, errors, and GPU
  health while the rollout manager evaluates the results.
3. **Promote:** Advance the rollout when the candidate remains healthy.
4. **Roll back to stable:** Remove the candidate and keep the known-good release
  serving traffic after repeated unhealthy evaluations.

## 1. Project Goals

- Release a new version gradually without interrupting the stable application.
- Decide whether to continue or stop a rollout using measured application and
  GPU health.
- Return traffic to the stable version automatically when the new version is
  repeatedly unhealthy.
- Keep the rollout safe when health data is unavailable or there is not enough
  traffic to make a reliable decision.
- Make the complete rollout, monitoring, failure-testing, and rollback process
  reproducible on a local machine.

## 2. Architecture Design

All components run in one local Kubernetes cluster. The diagram below shows how
requests, rollout actions, health signals, and test failures move between them.

![Runtime architecture showing traffic, rollout control, monitoring, and chaos injection inside one kind cluster](docs/runtime-architecture.svg)

### Reading the diagram

| Component | What it is | Responsibility in the diagram |
|---|---|---|
| Rollout manager | Custom Kubernetes controller | Reads Prometheus health results and requests rollout, promotion, or rollback changes. |
| Kubernetes API | Kubernetes control interface | Receives deployment and scaling requests from the rollout manager and applies them to the workload. |
| Local client | Process on the developer machine | Sends manual `/predict` requests through `kubectl port-forward`. |
| Inference API | Service endpoint and active application pods | Serves inference requests and exposes application metrics to Prometheus. |
| Chaos Mesh | Failure-injection system | Injects controlled pod, network, and CPU faults into the test workload. |
| Prometheus | Metrics database and query service | Collects application and GPU metrics and returns health results to the rollout manager. |
| Grafana | Dashboard service | Queries Prometheus and displays traffic, latency, health, and rollback state. |
| GPU exporter | Simulated metrics service | Publishes healthy or degraded GPU signals to Prometheus without requiring GPU hardware. |

## 3. Runtime Architecture

The logical design above is deployed into one local Kubernetes cluster. This
diagram shows the actual Kubernetes structure: cluster, node, namespaces,
workload controllers, Services, and the pods they manage.

![Kubernetes topology showing the health-rollout cluster, node, namespaces, Deployments, Services, and pods](docs/kubernetes-topology.svg)

### Kubernetes objects in the diagram

| Object | Plain-language meaning | What it does not do |
|---|---|---|
| **Pod** | The running application instance containing one or more containers. | It does not maintain or replace itself. |
| **Deployment** | A manager for a group of pods. It specifies the container image and desired replica count, then creates and replaces pods as needed. | It does not provide a stable network address. |
| **Service** | A stable network address that finds matching ready pods and sends requests to them. | It does not create pods or decide which version to deploy. |
| **Custom resource** | A project-specific settings record stored in Kubernetes. Here, `HealthGatedRollout` contains rollout steps and health limits. | It is not a pod, server, or running program. |

The rollout manager reads the `HealthGatedRollout` custom resource and updates
the stable and canary Deployments. Those Deployments create the pods, and the
`inference-service` Service routes requests to the ready pods.

The monitoring and Chaos Mesh sections also use **StatefulSets** for pods that
need stable identity and **DaemonSets** for one pod on every node.

| Scope | Count | Purpose |
|---|---:|---|
| Kubernetes clusters | 1 | Local cluster named `health-rollout` |
| Kubernetes nodes | 1 | `health-rollout-control-plane`, running the full stack |
| Project namespaces | 4 | `workload`, `rollout-system`, `monitoring`, `chaos-testing` |
| Rollout managers | 1 | Checks Prometheus health signals and scales Deployments |
| Inference tracks | 2 | Stable baseline and canary release candidate |
| Shared inference Service | 1 | Distributes requests across ready stable and canary pods |

| Functional area | Kubernetes namespace |
|---|---|
| Application | `workload` |
| Rollout automation | `rollout-system` |
| Monitoring | `monitoring` |
| Failure testing | `chaos-testing` |

The controller runs in `rollout-system`, separate from the application pods in
`workload`. Prometheus and Grafana run in `monitoring`, and Chaos Mesh runs in
`chaos-testing`. All pods are scheduled on the single kind control-plane node.

The Service does not decide whether stable or canary is deployed. The rollout
manager controls the Deployments through the Kubernetes API; the Service
discovers their ready pods and routes traffic to them. Stable and canary coexist
only during evaluation:

| State | Stable replicas | Canary replicas | Why |
|---|---:|---:|---|
| Before rollout | 1 | 0 | Only the known-good application serves traffic. |
| First canary step | 3 | 1 | Most requests reach stable; a smaller sample reaches canary. |
| Second canary step | 1 | 1 | Both receive approximately half of the requests. |
| Rollback | 1 | 0 | Canary is removed after repeated unhealthy checks. |
| Successful replacement | New version runs as stable | 0 | The validated version becomes stable and the temporary canary is removed. |

The current sample marks a healthy rollout promoted at the final `1/1` step.
The successful replacement shown above is the production completion that should
follow; that final handoff is not yet implemented by this project.

### 3.1 Traffic and ownership

- The stable and canary Deployments both use `app: inference-service`, so one
  Kubernetes Service sends traffic to every ready pod from both Deployments
  while both have replicas.
- Their distinct `track: stable` and `track: canary` labels are attached to
  application metrics. Prometheus can therefore evaluate the canary without
  mixing its measurements with the stable version.
- Traffic weighting is approximate and follows the replica ratio. The first
  rollout step uses three stable replicas and one canary replica, or roughly
  75% stable / 25% canary. The second step uses one of each, or roughly 50/50.
- The rollout manager owns the scaling decisions. It advances the canary when
  Prometheus reports healthy signals and restores stable when checks fail.
- Prometheus scrapes the inference service, simulated GPU exporter, Kubernetes
  state metrics, and the controller's rollback counter. Grafana reads the same
  data used by the controller, making the decision path observable.

### 3.2 What “good” and “bad” mean in this demo

The application test does not hide a scripted failure inside a separate bad
binary. Both Deployments use `inference-service:v0.1.0`, but they represent two
release configurations:

| Release | Initial replicas | CPU request | CPU limit | Purpose |
|---|---:|---:|---:|---|
| Stable / good | 1 | 200m | 1 core | Known-good baseline with enough inference capacity |
| Canary / degraded | 0 | 100m | 200m | Release candidate deliberately constrained under real MobileNet traffic |

The canary starts at zero replicas. When the rollout begins, the controller
deploys it at the first 3:1 step. The load generator continues to send real PNG
uploads to `/predict`; requests that land on the constrained canary produce
genuine latency or error measurements rather than fabricated application
responses.

The GPU-gated demonstration is a second, independent failure path. Its exporter
is clearly simulated and changes from a healthy profile (`58 C`, `0` ECC errors)
to a degraded profile (`96 C`, `8` ECC errors). The GPU rollout deliberately
sets permissive application thresholds so the recorded rollback can be
attributed specifically to the two GPU checks.

### 3.3 Automated demo sequence

This sequence shows what the external `demo.ps1` helper does to start and
exercise the system. The script is included here as an actor because this is an
automation timeline, not a diagram of components that remain running.

```mermaid
sequenceDiagram
    participant Demo as demo.ps1
    participant K8s as Kubernetes API
    participant Stable as Stable pods
    participant Canary as Canary pods
    participant Load as Load generator
    participant Prom as Prometheus
    participant Ctrl as Rollout controller

    Demo->>K8s: Deploy stable with 1 replica
    K8s->>Stable: Start known-good MobileNet service
    Demo->>K8s: Deploy canary with 0 replicas
    Demo->>K8s: Deploy shared Service and load generator
    Load->>Stable: Send real POST /predict traffic
    Stable-->>Prom: Export stable request and latency metrics

    Demo->>K8s: Start the rollout
    K8s-->>Ctrl: Notify the rollout controller
    Ctrl->>K8s: Scale stable:canary to 3:1
    K8s->>Canary: Start constrained release candidate
    Load->>Stable: Continue shared-Service traffic
    Load->>Canary: Route a share of real requests
    Canary-->>Prom: Export track=canary metrics

    loop Every 15 seconds
        Ctrl->>Prom: Query count, error rate, p95, and GPU checks
        Prom-->>Ctrl: Return one-minute-window values
    end

    Ctrl->>K8s: Require 2 consecutive unhealthy evaluations
    Ctrl->>K8s: Scale canary to 0 and stable to 1
    Ctrl->>K8s: Set phase=RolledBack and emit Event
    Ctrl-->>Prom: Increment rollback counter
    Prom-->>Grafana: Visualize traffic, health, and rollback
```

The deployment order matters. Prometheus and the load generator are running
before the rollout starts, giving the controller real traffic data as soon as
the canary becomes Ready. If Prometheus is unavailable, a query is empty, or
fewer than five canary requests exist in the one-minute window, the controller
holds the current step. It never promotes or rolls back from missing evidence.

### 3.4 How the metrics changed

The controller evaluates these signals from Prometheus:

| Signal | Healthy behavior | Degraded behavior | App rollout limit | GPU rollout limit |
|---|---|---|---:|---:|
| Canary request count | At least 5 requests in 1 minute | Same prerequisite | 5 minimum | 5 minimum |
| Canary HTTP 5xx rate | Near 0 | Rises if inference fails | 5% | 50% |
| Canary p95 latency | Stable remains much lower | Observed canary p95 reached 0.7651 s | 0.05 s | 10 s |
| Simulated GPU temperature | 58 C | 96 C | Not configured | 85 C |
| Simulated GPU ECC errors | 0 | 8 | Not configured | 1 |

The app-health rollout rolled back when real canary latency exceeded its limit:

```text
Health thresholds exceeded: latency 0.7651s > 0.0500s
```

The separately verified GPU rollout rolled back with both simulated signals in
the reason:

```text
Health thresholds exceeded: simulated GPU temperature 96.0000 > 85.0000,
simulated GPU ECC errors 8.0000 > 1.0000
```

### 3.5 Rollback decision

```mermaid
flowchart TD
    Start[Reconcile rollout] --> Scale[Apply current replica step]
    Scale --> Query[Query Prometheus after 15 seconds]
    Query --> Data{Prometheus available<br/>and at least 5 requests?}
    Data -->|No| Hold[Hold current step<br/>no promotion or rollback]
    Hold --> Query
    Data -->|Yes| Threshold{Any configured<br/>threshold exceeded?}
    Threshold -->|No| Advance{More rollout steps?}
    Advance -->|Yes| Scale
    Advance -->|No| Promote[Set phase Promoted]
    Threshold -->|Yes| Failures{2 consecutive<br/>failures?}
    Failures -->|No| Query
    Failures -->|Yes| Rollback[Scale stable to 1<br/>scale canary to 0]
    Rollback --> Record[Set phase RolledBack<br/>emit Event<br/>increment metric]
    Record --> Cooldown[Hold for 300 seconds]
```

Two failures must be separated by the configured 15-second evaluation interval;
extra reconciliations cannot accelerate the count. On rollback, the controller
restores `inference-stable` to one replica, scales `inference-canary` to zero,
sets `status.phase` to `RolledBack`, emits a `RolloutRolledBack` Event, increments
`health_gated_rollout_rollbacks_total`, and starts a five-minute cooldown.

**Test result: automatic rollback confirmed.** The dashboard shows the canary
scaling to zero, stable returning to one replica, and the rollback counter
incrementing after the unhealthy evaluations. It also shows the request rate
and p95 latency used during the test. The complete captured results and
simulated GPU view are in [docs/results/README.md](docs/results/README.md).

![Test result showing the canary rollback and stable recovery](docs/results/grafana-rollout-overview.png)

## 4. System Setup

Set up the system in this order:

1. Create the local `health-rollout` Kubernetes cluster with `kind`.
2. Create the namespaces used by the application and monitoring services.
3. Install Prometheus and Grafana for health checks and dashboards.
4. Build the inference API, load generator, GPU exporter, and rollout manager
  container images, then load them into the cluster.
5. Deploy the inference API, stable and canary workloads, load generator, and
  metrics exporters.
6. Deploy the rollout manager and connect it to the Kubernetes API and
  Prometheus.
7. Install Chaos Mesh and validate the failure experiments.
8. Start the rollout and observe whether it is promoted or rolled back.

## 5. Quick Start

Prerequisites: Docker Desktop, `kind`, `kubectl`, Helm, and PowerShell 7. The
demo does not require a GPU.

`demo.ps1` is a PowerShell helper that executes the setup and demonstration
steps described above. It:

1. Creates or reuses the local `health-rollout` kind cluster.
2. Installs Prometheus and Grafana with Helm.
3. Builds the inference service, load generator, GPU exporter, and rollout
  manager container images, then loads them into kind.
4. Applies the Kubernetes resources and waits for every Deployment to become
  ready.
5. Installs Chaos Mesh and validates the canary failure experiments.
6. Changes the simulated GPU exporter from healthy to degraded, starts a
  rollout, and waits until the rollout manager proves the rollback.
7. Prints the rollback reason, final stable/canary replica counts, and the
  command for opening Grafana.

```powershell
.\scripts\demo.ps1
```

The script stops if a required command fails and can be rerun against the named
cluster. It exits after the rollout reports `RolledBack`. See
[the captured results](docs/results/README.md).

## 6. Components

### 6.1 Rollout controller

The Go controller in [`controller/`](controller/) was built with Kubebuilder. It
watches `HealthGatedRollout` resources, scales the stable and canary Deployments,
queries Prometheus, records status conditions, emits Kubernetes Events, and
exports `health_gated_rollout_rollbacks_total`.

`HealthGatedRollout` is the project-specific Kubernetes configuration consumed
by the controller. It belongs to the Kubernetes setup rather than the high-level
runtime design:

```yaml
kind: HealthGatedRollout
spec:
  stableDeployment: inference-stable
  canaryDeployment: inference-canary
  steps:
    - stableReplicas: 3
      canaryReplicas: 1
  maxErrorRate: "0.05"
  failureThreshold: 2
```

This configuration names the two Deployments, sets the rollout replica steps,
and defines the health limits. It does not run code or receive traffic. The Go
controller reads it and performs the rollout actions.

Its sample policy uses two replica steps: 3 stable / 1 canary, followed by
1 stable / 1 canary. It evaluates one-minute Prometheus windows every 15 seconds,
requires at least five canary requests, and rolls back after two consecutive
unhealthy evaluations. Missing data causes a hold rather than a promotion or
rollback.

### 6.2 Inference service and traffic

[`inference-service/`](inference-service/) is a FastAPI application running
MobileNet through ONNX Runtime. Stable and canary are separate Deployments with
`track` labels, but one shared Service selects both. Their replica ratio provides
approximate traffic weighting without a service mesh.

The stable Deployment requests 200m CPU and can use up to one core. The canary
requests 100m and is limited to 200m, making it slower under real inference load.
Both expose request counters and latency histograms labeled by track, and both
include startup, readiness, and liveness probes plus restricted security
contexts.

### 6.3 Load generator

[`load-generator/`](load-generator/) contains a dependency-free Python process
that creates a PNG in memory and continuously uploads it to `POST /predict`.
`TARGET_URL`, `REQUESTS_PER_SECOND`, and `REQUEST_TIMEOUT_SECONDS` configure the
traffic without rebuilding the image.

### 6.4 Simulated GPU telemetry

[`gpu-metrics/`](gpu-metrics/) exposes clearly named `simulated_gpu_*` metrics.
Healthy mode reports 58 C and zero ECC errors; degraded mode reports 96 C and
eight ECC errors. These are test signals, not hardware measurements or DCGM
metrics. The controller evaluates them through the same configurable PromQL
check mechanism used for application signals.

### 6.5 Monitoring and dashboard

The Helm-installed kube-prometheus-stack runs in `monitoring`. ServiceMonitors
discover the inference service, GPU exporter, and controller. The checked-in
Grafana dashboard shows replica state, request rate, error rate, p95 latency,
rollback count, and simulated GPU health.

### 6.6 Failure injection

[`chaos/`](chaos/) contains scoped Chaos Mesh experiments for canary pod
termination, 250 ms outbound network delay, and one CPU stress worker at 80%
load. Every selector requires both `app: inference-service` and `track: canary`,
so the stable Deployment is not targeted.

### 6.7 Demo automation

[`scripts/demo.ps1`](scripts/demo.ps1) creates or reuses the cluster, installs
monitoring and pinned Chaos Mesh 2.8.0, builds and loads the local images,
deploys the complete system, validates the experiment manifests, switches the
simulated GPU exporter to degraded mode, and waits for a confirmed rollback.

## 7. Evidence

- [Runtime results and screenshots](docs/results/README.md)
- [One-command demo](scripts/demo.ps1)
- [Grafana dashboard manifest](dashboards/health-rollout-dashboard.yaml)
- [Chaos Mesh experiments](chaos/)

## 8. Tech Stack

- **Cluster:** one single-node kind cluster
- **Controller:** Go, Kubebuilder, controller-runtime, client-go
- **Inference workload:** Python, FastAPI, MobileNet, ONNX Runtime
- **Load generation:** custom dependency-free Python process
- **Metrics:** Prometheus, kube-prometheus-stack (Helm), Grafana
- **GPU test signals:** dependency-free Python Prometheus exporter
- **Failure injection:** Chaos Mesh 2.8.0
- **Automation:** PowerShell, Docker, kind, kubectl, Helm