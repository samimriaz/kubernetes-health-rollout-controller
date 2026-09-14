$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$clusterName = "health-rollout"
$monitoringChartVersion = "88.6.1"
$chaosMeshVersion = "2.8.0"

function Write-Step([string]$Message) {
    Write-Host "`n==> $Message" -ForegroundColor Cyan
}

foreach ($command in "docker", "kind", "kubectl", "helm") {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        throw "Required command '$command' was not found in PATH."
    }
}

Write-Step "Creating or reusing kind cluster '$clusterName'"
if ((kind get clusters) -notcontains $clusterName) {
    kind create cluster --name $clusterName --config .\cluster\kind-config.yaml
}
kubectl config use-context "kind-$clusterName" | Out-Null

Write-Step "Creating workload and monitoring namespaces"
kubectl apply -f .\kubernetes\workload\namespace.yaml
kubectl apply -f .\kubernetes\monitoring\namespace.yaml

Write-Step "Installing Prometheus and Grafana"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
helm upgrade --install monitoring prometheus-community/kube-prometheus-stack `
    --version $monitoringChartVersion `
    --namespace monitoring `
    --create-namespace `
    --set 'grafana.grafana\.ini.auth\.anonymous.enabled=true' `
    --set 'grafana.grafana\.ini.auth\.anonymous.org_role=Viewer' `
    --wait `
    --timeout 10m

Write-Step "Building local images"
docker build -t inference-service:v0.1.0 .\inference-service
docker build -t load-generator:v0.1.0 .\load-generator
docker build -t simulated-gpu-exporter:v0.1.0 .\gpu-metrics
docker build -t health-rollout-controller:v0.1.0 .\controller

Write-Step "Loading images into kind"
foreach ($image in @(
    "inference-service:v0.1.0",
    "load-generator:v0.1.0",
    "simulated-gpu-exporter:v0.1.0",
    "health-rollout-controller:v0.1.0"
)) {
    kind load docker-image $image --name $clusterName
}

Write-Step "Deploying inference workload and metric exporters"
kubectl apply -f .\kubernetes\workload\stable-deployment.yaml
kubectl apply -f .\kubernetes\workload\canary-deployment.yaml
kubectl apply -f .\kubernetes\workload\service.yaml
kubectl apply -f .\kubernetes\workload\load-generator-deployment.yaml
kubectl apply -f .\kubernetes\monitoring\inference-service-monitor.yaml
kubectl apply -f .\kubernetes\monitoring\simulated-gpu-exporter.yaml
kubectl rollout restart deployment/inference-stable deployment/load-generator -n workload
kubectl rollout restart deployment/simulated-gpu-exporter -n monitoring
kubectl rollout status deployment/inference-stable -n workload --timeout=5m
kubectl rollout status deployment/load-generator -n workload --timeout=3m
kubectl rollout status deployment/simulated-gpu-exporter -n monitoring --timeout=2m

Write-Step "Deploying rollout controller and Grafana dashboard"
kubectl apply -k .\controller\config\default
kubectl rollout restart deployment/rollout-controller-manager -n rollout-system
kubectl rollout status deployment/rollout-controller-manager -n rollout-system --timeout=3m
kubectl apply -f .\dashboards\health-rollout-dashboard.yaml

Write-Step "Installing Chaos Mesh and validating canary experiments"
helm repo add chaos-mesh https://charts.chaos-mesh.org --force-update
helm upgrade --install chaos-mesh chaos-mesh/chaos-mesh `
    --version $chaosMeshVersion `
    --namespace chaos-testing `
    --create-namespace `
    --set dashboard.create=false `
    --set controllerManager.replicaCount=1 `
    --set chaosDaemon.runtime=containerd `
    --set chaosDaemon.socketPath=/run/containerd/containerd.sock `
    --wait `
    --timeout 5m
kubectl apply -k .\chaos --dry-run=server

Write-Step "Triggering a simulated GPU health rollback"
kubectl delete healthgatedrollout inference-rollout gpu-health-rollout -n workload --ignore-not-found=true
kubectl set env deployment/simulated-gpu-exporter -n monitoring SIMULATED_GPU_MODE=degraded
kubectl rollout status deployment/simulated-gpu-exporter -n monitoring --timeout=2m
kubectl apply -f .\controller\config\samples\rollout_v1alpha1_gpu_health.yaml
kubectl wait --for=jsonpath='{.status.phase}'=RolledBack `
    healthgatedrollout/gpu-health-rollout `
    -n workload `
    --timeout=3m

Write-Step "Rollback result"
$rollout = kubectl get healthgatedrollout gpu-health-rollout -n workload -o json | ConvertFrom-Json
Write-Host "phase=$($rollout.status.phase)"
Write-Host "reason=$($rollout.status.conditions[-1].message)"
kubectl get deployments inference-stable inference-canary -n workload `
    -o custom-columns='NAME:.metadata.name,DESIRED:.spec.replicas,READY:.status.readyReplicas'

Write-Host "`nDemo complete." -ForegroundColor Green
Write-Host "Grafana: kubectl port-forward -n monitoring service/monitoring-grafana 3000:80"
Write-Host "Dashboard: http://127.0.0.1:3000/d/health-gated-rollout/health-gated-canary-rollout"