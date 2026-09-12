import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


MODE_VALUES = {
    "healthy": {
        "utilization": 62.0,
        "temperature": 58.0,
        "ecc_errors": 0.0,
    },
    "degraded": {
        "utilization": 99.0,
        "temperature": 96.0,
        "ecc_errors": 8.0,
    },
}


def current_mode() -> str:
    mode = os.getenv("SIMULATED_GPU_MODE", "healthy").lower()
    return mode if mode in MODE_VALUES else "healthy"


def render_metrics(mode: str) -> str:
    values = MODE_VALUES[mode]
    return "\n".join(
        [
            "# HELP simulated_gpu_utilization_percent Simulated GPU utilization percentage.",
            "# TYPE simulated_gpu_utilization_percent gauge",
            f'simulated_gpu_utilization_percent{{mode="{mode}"}} {values["utilization"]}',
            "# HELP simulated_gpu_temperature_celsius Simulated GPU temperature in Celsius.",
            "# TYPE simulated_gpu_temperature_celsius gauge",
            f'simulated_gpu_temperature_celsius{{mode="{mode}"}} {values["temperature"]}',
            "# HELP simulated_gpu_ecc_errors_total Simulated cumulative GPU ECC errors.",
            "# TYPE simulated_gpu_ecc_errors_total gauge",
            f'simulated_gpu_ecc_errors_total{{mode="{mode}"}} {values["ecc_errors"]}',
            "",
        ]
    )


class MetricsHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path == "/health":
            self._respond(200, "application/json", json.dumps({"status": "healthy"}))
            return
        if self.path == "/metrics":
            self._respond(200, "text/plain; version=0.0.4", render_metrics(current_mode()))
            return
        self._respond(404, "application/json", json.dumps({"detail": "Not Found"}))

    def _respond(self, status: int, content_type: str, body: str) -> None:
        encoded = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, format: str, *args: object) -> None:
        return


def main() -> None:
    port = int(os.getenv("PORT", "9101"))
    ThreadingHTTPServer(("0.0.0.0", port), MetricsHandler).serve_forever()


if __name__ == "__main__":
    main()