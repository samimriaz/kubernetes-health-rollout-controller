"""HTTP API for the inference workload.

FastAPI is a Python framework for building web APIs. Here it creates the HTTP
service that Kubernetes will run inside each stable or canary container.

This version provides:
- /health for Kubernetes health checks.
- /metrics for Prometheus monitoring.
- middleware that measures application requests.
- /predict for real MobileNet image classification.
"""

import os
import time
from pathlib import Path

# FastAPI creates the web application and maps Python functions to HTTP routes.
# UploadFile receives an uploaded image without treating it as JSON.
from fastapi import FastAPI, File, HTTPException, Request, Response, UploadFile
# prometheus_client creates metrics in the text format that Prometheus scrapes.
from prometheus_client import CONTENT_TYPE_LATEST, CollectorRegistry, Counter, Histogram, generate_latest

from app.model import InvalidImageError, MobileNetClassifier

# Kubernetes will set TRACK in each Deployment to identify its rollout role:
# - stable: the current trusted application version.
# - canary: the new candidate version being evaluated before promotion.
#
# The MVP will use 10 total application replicas behind one shared Service.
# Kubernetes distributes requests across them, so replica ratios approximate
# traffic percentages:
# - Rollout starts at 9 stable + 1 canary (about 10% canary traffic).
# - If healthy, it advances to 5 stable + 5 canary (about 50% canary traffic).
# - If still healthy, it advances to 0 stable + 10 canary (100% canary traffic).
# - On failure, it rolls back to 10 stable + 0 canary.
# - On success, the canary version is promoted and becomes the new stable.
#
# Keeping TRACK on every metric lets Prometheus evaluate canary health without
# mixing its measurements with those from stable instances.
TRACK = os.getenv("TRACK", "stable")

# Resolve the model relative to this source file instead of the terminal's
# working directory, so local runs and containers find the same file.
MODEL_PATH = Path(__file__).resolve().parents[1] / "models" / "mobilenetv2-7.onnx"

# Loading once at process startup avoids reloading 14 MB of model weights for
# every request. If loading fails, the process stays up but readiness returns
# HTTP 503, so Kubernetes will not route application traffic to this pod.
try:
    classifier: MobileNetClassifier | None = MobileNetClassifier(MODEL_PATH)
    model_error: str | None = None
except Exception as error:
    classifier = None
    model_error = str(error)

# A dedicated registry contains only this application's metrics. This keeps the
# /metrics response predictable while we learn and test the service.
registry = CollectorRegistry()
request_count = Counter(
    # A Counter only increases. We use it to calculate request and error rates.
    "inference_http_requests_total",
    "Total HTTP requests handled by the inference service.",
    ("track", "method", "path", "status"),
    registry=registry,
)
request_latency = Histogram(
    # A Histogram groups durations into buckets so Prometheus can calculate
    # percentiles such as p95 latency for rollout decisions.
    "inference_http_request_duration_seconds",
    "HTTP request duration in seconds.",
    ("track", "method", "path"),
    registry=registry,
)

# This object is the actual web application served by Uvicorn. Decorators such
# as @app.get below register URL paths and their handler functions.
app = FastAPI(title="Inference Service", version="0.1.0")


# Middleware wraps every HTTP request, which gives us one place to measure all
# future endpoints, including /predict.
@app.middleware("http")
async def record_request_metrics(request: Request, call_next):
    # call_next sends the request to the matching endpoint and returns its
    # response. Measuring around it captures the endpoint's complete duration.
    start_time = time.perf_counter()
    response = await call_next(request)

    path = request.url.path
    # Health probes and Prometheus scrapes are infrastructure traffic. Excluding
    # them prevents those calls from distorting inference health measurements.
    if path not in {"/health", "/metrics"}:
        request_count.labels(TRACK, request.method, path, response.status_code).inc()
        request_latency.labels(TRACK, request.method, path).observe(
            time.perf_counter() - start_time
        )

    return response


# Kubernetes readiness and liveness probes will call this endpoint later.
@app.get("/health")
def health(response: Response) -> dict[str, str]:
    # Readiness now depends on the real model. HTTP 503 tells Kubernetes not to
    # send prediction traffic when model loading failed.
    if classifier is None:
        response.status_code = 503
        return {"status": "unhealthy", "track": TRACK, "reason": model_error or "unknown"}

    return {"status": "healthy", "track": TRACK}


# Clients upload a JPEG or PNG as multipart form data under the field "file".
# FastAPI calls this function for POST /predict requests.
@app.post("/predict")
async def predict(file: UploadFile = File(...)) -> dict[str, str | int | float]:
    if classifier is None:
        raise HTTPException(status_code=503, detail="The model is not ready.")

    image_bytes = await file.read()
    try:
        prediction = classifier.predict(image_bytes)
    except InvalidImageError as error:
        raise HTTPException(status_code=400, detail=str(error)) from error

    return {"track": TRACK, **prediction}


# Prometheus scrapes this endpoint and expects its text exposition format.
@app.get("/metrics")
def metrics() -> Response:
    # generate_latest serializes the current counters and histograms into the
    # Prometheus exposition format rather than ordinary JSON.
    return Response(content=generate_latest(registry), media_type=CONTENT_TYPE_LATEST)
