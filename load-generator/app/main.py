"""Continuously send real image-classification requests to the service.

The generator intentionally uses only Python's standard library. That keeps
its container small and avoids downloading packages during the Docker build.
Kubernetes environment variables control the target, rate, and timeout.
"""

import json
import os
import struct
import time
import urllib.error
import urllib.request
import zlib


TARGET_URL = os.getenv(
    "TARGET_URL", "http://inference-service:8000/predict"
)
REQUESTS_PER_SECOND = float(os.getenv("REQUESTS_PER_SECOND", "1"))
REQUEST_TIMEOUT_SECONDS = float(os.getenv("REQUEST_TIMEOUT_SECONDS", "10"))


def create_png(width: int = 224, height: int = 224) -> bytes:
    """Create a valid RGB PNG without requiring an image library."""

    # Each row starts with PNG filter byte 0, followed by RGB pixels. A simple
    # color pattern gives MobileNet real image data rather than empty bytes.
    rows = bytearray()
    for y_position in range(height):
        rows.append(0)
        for x_position in range(width):
            rows.extend(
                (
                    x_position * 255 // width,
                    y_position * 255 // height,
                    128,
                )
            )

    def chunk(chunk_type: bytes, data: bytes) -> bytes:
        checksum = zlib.crc32(chunk_type + data) & 0xFFFFFFFF
        return struct.pack(">I", len(data)) + chunk_type + data + struct.pack(">I", checksum)

    header = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", header)
        + chunk(b"IDAT", zlib.compress(bytes(rows)))
        + chunk(b"IEND", b"")
    )


def create_multipart_body(image: bytes) -> tuple[bytes, str]:
    """Wrap the PNG in the multipart field expected by POST /predict."""

    boundary = "load-generator-boundary"
    body = (
        f"--{boundary}\r\n"
        'Content-Disposition: form-data; name="file"; filename="load.png"\r\n'
        "Content-Type: image/png\r\n\r\n"
    ).encode("ascii") + image + f"\r\n--{boundary}--\r\n".encode("ascii")
    return body, f"multipart/form-data; boundary={boundary}"


def send_prediction(image: bytes) -> None:
    """Send one prediction request and print its result for pod logs."""

    body, content_type = create_multipart_body(image)
    request = urllib.request.Request(
        TARGET_URL,
        data=body,
        headers={"Content-Type": content_type},
        method="POST",
    )

    started_at = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
            result = json.loads(response.read())
            elapsed = time.perf_counter() - started_at
            print(
                f"status={response.status} track={result.get('track')} "
                f"class={result.get('class_index')} duration={elapsed:.3f}s",
                flush=True,
            )
    except urllib.error.HTTPError as error:
        print(f"status={error.code} error={error.reason}", flush=True)
    except (urllib.error.URLError, TimeoutError) as error:
        print(f"request_failed error={error}", flush=True)


def main() -> None:
    """Run at the configured average request rate until Kubernetes stops us."""

    if REQUESTS_PER_SECOND <= 0:
        raise ValueError("REQUESTS_PER_SECOND must be greater than zero")

    interval_seconds = 1 / REQUESTS_PER_SECOND
    image = create_png()
    print(
        f"Starting load generator: target={TARGET_URL} "
        f"rate={REQUESTS_PER_SECOND} requests/second",
        flush=True,
    )

    while True:
        request_started_at = time.monotonic()
        send_prediction(image)

        # Subtract request time so a slow response does not add a second full
        # interval. One process remains sequential and intentionally simple.
        elapsed = time.monotonic() - request_started_at
        time.sleep(max(0, interval_seconds - elapsed))


if __name__ == "__main__":
    main()