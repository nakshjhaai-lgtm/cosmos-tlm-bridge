"""Downstream ground-microservice stand-in used by docker compose.

Consumes the typed ``TelemetryFrame`` webhook payload and reports what it saw.
This is what a Go/Python/React ground service does with the bridge output —
it never touches COSMOS, Ruby, or a proprietary SDK.
"""

from __future__ import annotations

import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("SINK_PORT", "8080"))
# A finite window lets the one-command compose demo exit on its own.
DURATION = float(os.environ.get("SINK_DURATION", "0"))
STATE = {
    "frames": 0,
    "points": 0,
    "by_packet": {},
    "states": {},
    "alarms": [],
    "last_overall": "GREEN",
}
LOCK = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):  # quiet by default
        return

    def _json(self, status: int, doc: dict) -> None:
        body = json.dumps(doc).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        with LOCK:
            self._json(200, {"status": "ok", **STATE})

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length") or 0)
        payload = json.loads(self.rfile.read(length) or b"{}")
        points = payload.get("points", {})
        with LOCK:
            STATE["frames"] += 1
            STATE["points"] += len(points)
            packet = payload.get("packet", "?")
            STATE["by_packet"][packet] = STATE["by_packet"].get(packet, 0) + 1
            STATE["last_overall"] = payload.get("limits_overall", "GREEN")
            for name, p in points.items():
                state = p.get("limits_state", "UNKNOWN")
                STATE["states"][state] = STATE["states"].get(state, 0) + 1
                if p.get("limits_state") in {
                    "YELLOW_LOW",
                    "YELLOW_HIGH",
                    "RED_LOW",
                    "RED_HIGH",
                }:
                    if len(STATE["alarms"]) < 200:
                        STATE["alarms"].append(
                            {
                                "channel": p.get("channel"),
                                "dn": p.get("dn"),
                                "eu": p.get("eu"),
                                "limits_state": state,
                                "packet": packet,
                            }
                        )
            print(
                f"[sink] {packet:<8} overall={payload.get('limits_overall'):<6} "
                f"cosmos={payload.get('cosmos_overall_limits_state'):<6} points={len(points)}",
                flush=True,
            )
        self._json(200, {"received": True})


if __name__ == "__main__":
    print(f"[sink] listening on 0.0.0.0:{PORT}", flush=True)
    httpd = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)

    if DURATION > 0:
        def stop() -> None:
            print(f"[sink] demo window complete after {DURATION:g}s", flush=True)
            httpd.shutdown()

        timer = threading.Timer(DURATION, stop)
        timer.daemon = True
        timer.start()
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        httpd.server_close()
    print(
        f"[sink] frames={STATE['frames']} points={STATE['points']} "
        f"last_overall={STATE['last_overall']}",
        flush=True,
    )
    sys.exit(0)
