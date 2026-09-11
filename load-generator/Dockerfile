# The generator uses only Python's standard library, so no package installation
# or external network access is required during this image build.
FROM python:3.12-slim

# Flush each request result directly to kubectl logs and avoid cache files.
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

WORKDIR /app

# Only runtime code is copied. Tests remain on the development machine.
COPY app/ ./app/

# Run with an unprivileged identity rather than the image's root user.
RUN useradd --create-home --uid 10001 appuser \
    && chown -R appuser:appuser /app
USER appuser

# Kubernetes restarts this process if it exits unexpectedly.
CMD ["python", "app/main.py"]