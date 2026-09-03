# Start from a small Linux image that already contains Python 3.12.
FROM python:3.12-slim

# Python logs appear immediately in container logs, and Python does not create
# __pycache__ files inside the running container.
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

# All following commands and the final application run from /app.
WORKDIR /app

# Copy and install dependencies before application code. wheelhouse contains
# Linux packages downloaded through the host's configured package feed, so the
# Docker build does not require network access or package-feed credentials.
COPY requirements.txt .
COPY wheelhouse/ ./wheelhouse/
RUN python -m pip install --no-cache-dir --no-index \
    --find-links=/app/wheelhouse -r requirements.txt \
    && rm -rf /app/wheelhouse

# Copy the FastAPI source and trained MobileNet weights into the image.
COPY app/ ./app/
COPY models/ ./models/

# Document the port used by Uvicorn. Kubernetes will target this port later.
EXPOSE 8000

# Run as an unprivileged user rather than root inside the container.
RUN useradd --create-home --uid 10001 appuser \
    && chown -R appuser:appuser /app
USER appuser

# 0.0.0.0 allows traffic from outside the container. One worker keeps metrics
# local to one process, which is appropriate because Kubernetes scales pods.
CMD ["python", "-m", "uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1"]
