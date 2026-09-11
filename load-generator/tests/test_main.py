"""Tests for the dependency-free load generator."""

import importlib.util
import struct
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


# The directory contains a hyphen, so load the script by file path instead of
# importing it as a normal Python package.
MODULE_PATH = Path(__file__).parents[1] / "app" / "main.py"
SPEC = importlib.util.spec_from_file_location("load_generator_main", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
load_generator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(load_generator)


class PredictionHandler(BaseHTTPRequestHandler):
    """Capture one request and return the inference service's JSON shape."""

    content_type = ""
    request_body = b""

    def do_POST(self) -> None:
        content_length = int(self.headers["Content-Length"])
        type(self).content_type = self.headers["Content-Type"]
        type(self).request_body = self.rfile.read(content_length)

        response = b'{"track":"stable","class_index":42}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(response)))
        self.end_headers()
        self.wfile.write(response)

    def log_message(self, format: str, *args: object) -> None:
        # Suppress HTTP access logs so test output stays focused.
        return


class LoadGeneratorTests(unittest.TestCase):
    def test_create_png_has_valid_signature_and_dimensions(self) -> None:
        image = load_generator.create_png(width=16, height=12)

        self.assertEqual(image[:8], b"\x89PNG\r\n\x1a\n")
        self.assertEqual(struct.unpack(">II", image[16:24]), (16, 12))

    def test_send_prediction_uploads_png_as_file_field(self) -> None:
        server = HTTPServer(("127.0.0.1", 0), PredictionHandler)
        server_thread = threading.Thread(target=server.handle_request)
        server_thread.start()

        original_target = load_generator.TARGET_URL
        load_generator.TARGET_URL = (
            f"http://127.0.0.1:{server.server_port}/predict"
        )
        try:
            load_generator.send_prediction(load_generator.create_png(8, 8))
        finally:
            load_generator.TARGET_URL = original_target
            server_thread.join(timeout=2)
            server.server_close()

        self.assertIn("multipart/form-data", PredictionHandler.content_type)
        self.assertIn(b'name="file"', PredictionHandler.request_body)
        self.assertIn(b"filename=\"load.png\"", PredictionHandler.request_body)
        self.assertIn(b"\x89PNG\r\n\x1a\n", PredictionHandler.request_body)


if __name__ == "__main__":
    unittest.main()