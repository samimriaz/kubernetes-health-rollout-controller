FROM python:3.12-slim

WORKDIR /app
COPY app ./app

RUN useradd --create-home --uid 10001 exporter
USER 10001

EXPOSE 9101
CMD ["python", "-m", "app.main"]