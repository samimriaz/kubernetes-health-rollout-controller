from io import BytesIO

from fastapi.testclient import TestClient
from PIL import Image

from app.main import app

# TestClient calls the FastAPI application in-process, so these tests do not
# need a running web server or Kubernetes cluster.
client = TestClient(app)


def create_test_image() -> bytes:
    """Create a real PNG in memory so tests need no external image file."""
    buffer = BytesIO()
    Image.new("RGB", (320, 240), color=(120, 80, 40)).save(buffer, format="PNG")
    return buffer.getvalue()


def test_health_reports_stable_track() -> None:
    response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "healthy", "track": "stable"}


def test_metrics_include_track_label() -> None:
    # Generate one measured request before scraping /metrics. A missing route is
    # useful here because middleware still records its real HTTP 404 response.
    client.get("/not-found")

    response = client.get("/metrics")

    assert response.status_code == 200
    # The track and path labels are the contract used by future PromQL queries.
    assert "inference_http_requests_total" in response.text
    assert 'track="stable"' in response.text
    assert 'path="/not-found"' in response.text


def test_predict_runs_real_mobilenet_inference() -> None:
    response = client.post(
        "/predict",
        files={"file": ("sample.png", create_test_image(), "image/png")},
    )

    assert response.status_code == 200
    prediction = response.json()
    assert prediction["track"] == "stable"
    assert 0 <= prediction["class_index"] < 1000
    assert 0.0 <= prediction["confidence"] <= 1.0


def test_predict_rejects_invalid_image() -> None:
    response = client.post(
        "/predict",
        files={"file": ("not-an-image.txt", b"not an image", "text/plain")},
    )

    assert response.status_code == 400
    assert response.json() == {"detail": "The uploaded file is not a valid image."}
