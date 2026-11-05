"""A Docker-friendly, dependency-free stand-in for an OpenC3 COSMOS v5
command-and-telemetry server serving the ``DROID_SIM`` target.

It speaks the real OpenC3 surfaces the bridge depends on:

===============================  =============================================
``POST /openc3-api/auth/verify``  COSMOS Core password verification
``POST /__openc3_api__/api``      JSON-RPC 2.0 (Enterprise / proxied path)
``POST /openc3-api/api``          JSON-RPC 2.0 (COSMOS Core path)
``GET  /openc3-api/stream``       StreamingApi topic subscription (NDJSON)
``GET  /openc3-api/health``       liveness
``GET  /metrics``                 Prometheus-style counters
===============================  =============================================

The JSON-RPC surface implements the subset of the COSMOS
``tlm``/``cmd``/limits APIs that ground software uses.
"""

from __future__ import annotations

import json
import threading
import time
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

from ..models import LimitsState, TargetDefinition, load_target
from ..telemetry import TelemetryFrame
from .droid import DroidSimulator

__all__ = ["MockCosmosServer", "serve"]

PKTS = ("ADCS", "EPS", "THERMAL")


def _definitions_dir() -> Path:
    """Locate the bundled OpenC3 target definitions."""
    here = Path(__file__).resolve()
    candidates = [
        here.parents[1] / "_bundled" / "definitions" / "droid_sim",  # installed wheel
        here.parents[3] / "definitions" / "droid_sim",               # source checkout
        Path.cwd() / "definitions" / "droid_sim",
        Path("/app/definitions/droid_sim"),                          # container
    ]
    for candidate in candidates:
        if candidate.is_dir():
            return candidate
    raise FileNotFoundError(
        "could not locate definitions/droid_sim; set COSMOS_DEFINITIONS_DIR"
    )


class MockCosmosServer:
    """Threaded HTTP server exposing DROID_SIM telemetry over OpenC3's API."""

    def __init__(
        self,
        host: str = "0.0.0.0",
        port: int = 2900,
        *,
        scope: str = "DEFAULT",
        seed: int = 1337,
        definitions_dir: str | Path | None = None,
        auth_required: bool = False,
        password: str = "cosmos",
        stream_rate_hz: float = 4.0,
    ) -> None:
        import os

        self.host = host
        self.port = int(os.environ.get("COSMOS_PORT", port))
        self.scope = scope.upper()
        self.auth_required = auth_required
        self.password = password
        self.stream_rate_hz = stream_rate_hz
        self.definitions_dir = (
            Path(definitions_dir)
            if definitions_dir
            else Path(os.environ.get("COSMOS_DEFINITIONS_DIR", "")) if os.environ.get("COSMOS_DEFINITIONS_DIR") else _definitions_dir()
        )
        self.target_def: TargetDefinition = load_target(self.definitions_dir, "DROID_SIM")
        self.seed = seed
        self.sim = DroidSimulator(seed=seed)
        self.tokens: set[str] = set()
        self.metrics = {"rpc_requests": 0, "stream_frames": 0, "commands": 0, "errors": 0}
        self.started_at = time.time()
        self._httpd: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self._iface: threading.Thread | None = None
        self._iface_stop = threading.Event()

    # -- frame helpers ----------------------------------------------------- #

    def frame(self, packet: str) -> TelemetryFrame:
        dn = self.sim.packet_dn(packet)  # read from the interface-written CVT
        frame = TelemetryFrame.from_dn(
            self.target_def,
            packet,
            dn,
            scope=self.scope,
            received_time=time.time(),
            received_count=self.sim.sequence[packet.upper()],
            limits_set=self.sim.limits_set,
        )
        self.sim.record_limits_events(
            packet.upper(), {k: v.limits_state.value for k, v in frame.points.items()}
        )
        return frame

    def _all_frames(self) -> dict[str, TelemetryFrame]:
        return {p: self.frame(p) for p in PKTS}

    # -- JSON-RPC dispatch -------------------------------------------------- #

    def dispatch(self, method: str, params: list[Any], keyword: dict[str, Any]) -> Any:
        self.metrics["rpc_requests"] += 1
        value_type = str(keyword.get("type", "CONVERTED")).upper()
        scope = str(keyword.get("scope", self.scope)).upper()

        if method == "get_server_status":
            return {
                "status": "ok",
                "scope": scope,
                "targets": [self.target_def.name],
                "packets": list(PKTS),
                "uptime_s": round(time.time() - self.started_at, 3),
                "version": "openc3-cosmos-init/5.0.0-droid-sim",
                "auth_required": self.auth_required,
            }

        if method in {"tlm", "tlm_raw", "tlm_formatted", "tlm_with_units"}:
            target, packet, item = _split_tlm_params(params)
            vt = {
                "tlm_raw": "RAW",
                "tlm_formatted": "FORMATTED",
                "tlm_with_units": "FORMATTED",
                "tlm": value_type,
            }[method]
            point = self.frame(packet).point(item)
            if vt == "RAW":
                return point.dn
            if vt == "FORMATTED":
                if method == "tlm_formatted":
                    return point.formatted.split(" ")[0]
                return point.formatted
            return point.eu

        if method == "get_tlm_packet":
            target, packet = str(params[0]).upper(), str(params[1]).upper()
            frame = self.frame(packet)
            return [
                [name, _value_for(point, value_type), point.limits_state.value]
                for name, point in frame.points.items()
            ]

        if method == "get_tlm_values":
            items = params[0]
            grouped: dict[str, list[tuple[str, str]]] = {}
            for target, packet, item in items:
                grouped.setdefault(packet.upper(), []).append((item.upper(), packet.upper()))
            values: list[Any] = []
            states: list[str] = []
            settings: list[list[Any]] = []
            for packet, entries in grouped.items():
                frame = self.frame(packet)
                for item, _ in entries:
                    point = frame.point(item)
                    values.append(_value_for(point, value_type))
                    states.append(point.limits_state.value)
                    lim = point.limits or {}
                    settings.append(
                        [
                            point.limits_set,
                            1,
                            "ENABLED",
                            lim.get("red_low"),
                            lim.get("yellow_low"),
                            lim.get("yellow_high"),
                            lim.get("red_high"),
                            lim.get("green_low"),
                            lim.get("green_high"),
                        ]
                    )
            return [values, states, settings, self.sim.limits_set]

        if method == "get_tlm_list":
            return [[p, self.target_def.packet(p).description] for p in PKTS]

        if method == "get_tlm_cnt":
            return self.sim.sequence[str(params[1]).upper()]

        if method == "get_limits":
            target, packet, item = str(params[0]).upper(), str(params[1]).upper(), str(params[2]).upper()
            point = self.frame(packet).point(item)
            lim = point.limits or {}
            return [
                point.limits_set,
                1,
                "ENABLED",
                lim.get("red_low"),
                lim.get("yellow_low"),
                lim.get("yellow_high"),
                lim.get("red_high"),
                lim.get("green_low"),
                lim.get("green_high"),
            ]

        if method == "get_out_of_limits":
            out: list[list[Any]] = []
            for packet, frame in self._all_frames().items():
                for name, point in frame.points.items():
                    if point.out_of_limits:
                        out.append(
                            [self.target_def.name, packet, name, point.limits_state.value]
                        )
            return out

        if method == "get_overall_limits_state":
            states = [f.limits_summary().overall.value for f in self._all_frames().values()]
            for level in ("RED", "YELLOW", "BLUE", "GREEN"):
                if level in states:
                    return level
            return "GREEN"

        if method in {"cmd", "cmd_no_checks"}:
            cmd_string = str(params[0])
            result = self.sim.send_command(cmd_string)
            self.metrics["commands"] += 1
            return {**result, "cmd_string": cmd_string, "check": method == "cmd"}

        if method == "get_cmd_cnt":
            return sum(1 for c in self.sim.commands if c["name"] == str(params[1]).upper())

        if method == "get_cmd_time":
            matches = [c for c in self.sim.commands if c["name"] == str(params[1]).upper()]
            if not matches:
                return [str(params[0]), str(params[1]), None]
            return [str(params[0]), str(params[1]), matches[-1]["time"]]

        if method == "inject_tlm":
            # Accepted for parity with Script Runner; DROID_SIM is self-driving.
            return None

        if method == "droid_sim_snapshot":
            return self.sim.snapshot()

        if method == "droid_sim_reset":
            self.sim = DroidSimulator(seed=self.seed)
            return {"reset": True, "target": self.target_def.name}

        if method == "droid_sim_limits_events":
            return self.sim.limits_events[-50:]

        raise _RpcFault(f"unknown JSON-RPC method {method!r}")

    # -- lifecycle --------------------------------------------------------- #

    def _make_handler(self) -> type[BaseHTTPRequestHandler]:
        server = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            server_version = "OpenC3COSMOS-DROID_SIM/5.0"

            def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A003
                if server.metrics.get("verbose"):
                    print(f"[droid-sim] {fmt % args}")

            # -- helpers --
            def _send(self, status: int, body: bytes, ctype: str = "application/json") -> None:
                self.send_response(status)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(body)))
                self.send_header("Access-Control-Allow-Origin", "*")
                self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def _json(self, status: int, doc: Any) -> None:
                self._send(status, json.dumps(doc).encode())

            def _authorized(self) -> bool:
                if not server.auth_required:
                    return True
                header = self.headers.get("Authorization", "")
                return header.strip() in server.tokens

            def do_OPTIONS(self) -> None:  # noqa: N802
                self.send_response(204)
                self.send_header("Access-Control-Allow-Origin", "*")
                self.send_header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
                self.send_header("Access-Control-Allow-Headers", "Content-Type, Authorization")
                self.send_header("Content-Length", "0")
                self.end_headers()

            def do_GET(self) -> None:  # noqa: N802
                parsed = urllib.parse.urlparse(self.path)
                path = parsed.path.rstrip("/") or "/"

                if path in {"/health", "/openc3-api/health", "/healthz"}:
                    return self._json(
                        200,
                        {
                            "status": "ok",
                            "target": "DROID_SIM",
                            "scope": server.scope,
                            "uptime_s": round(time.time() - server.started_at, 3),
                        },
                    )

                if path == "/metrics":
                    lines = [f"cosmos_droid_sim_{k} {v}" for k, v in sorted(server.metrics.items()) if isinstance(v, int)]
                    lines.append(
                        f"cosmos_droid_sim_uptime_seconds {round(time.time() - server.started_at, 3)}"
                    )
                    return self._send(200, ("\n".join(lines) + "\n").encode(), "text/plain")

                if path in {"/openc3-api/stream", "/__openc3_api__/stream"}:
                    return self._stream(parsed.query)

                if path in {"/", "/openc3-api"}:
                    return self._json(
                        200,
                        {
                            "service": "DROID_SIM OpenC3 COSMOS v5 reference server",
                            "api": ["/openc3-api/api", "/__openc3_api__/api"],
                            "auth": "/openc3-api/auth/verify",
                            "stream": "/openc3-api/stream?topic=DEFAULT__DECOM__{DROID_SIM}__EPS",
                            "targets": ["DROID_SIM"],
                        },
                    )

                return self._json(404, {"error": f"no route {path}"})

            def _stream(self, query: str) -> None:
                params = urllib.parse.parse_qs(query)
                topic = params.get("topic", [""])[0]
                # SCOPE__DECOM__{TARGET}__PACKET
                parts = [p.strip("{}") for p in topic.split("__")]
                packet = parts[-1].upper() if parts else "EPS"
                if packet not in PKTS:
                    packet = "EPS"
                try:
                    rate = float(params.get("rate", [server.stream_rate_hz])[0])
                except ValueError:
                    rate = server.stream_rate_hz
                try:
                    limit = int(params.get("limit", ["0"])[0])
                except ValueError:
                    limit = 0

                self.send_response(200)
                self.send_header("Content-Type", "application/x-ndjson")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Access-Control-Allow-Origin", "*")
                self.end_headers()
                sent = 0
                interval = 1.0 / max(rate, 0.5)
                try:
                    while True:
                        payload = {
                            "topic": f"{server.scope}__DECOM__{{DROID_SIM}}__{packet}",
                            "packet": packet,
                            "time": int(time.time() * 1e9),
                            "frame": server.frame(packet).to_webhook_payload(),
                        }
                        self.wfile.write((json.dumps(payload) + "\n").encode())
                        self.wfile.flush()
                        server.metrics["stream_frames"] += 1
                        sent += 1
                        if limit and sent >= limit:
                            return
                        time.sleep(interval)
                except (BrokenPipeError, ConnectionResetError):
                    return

            def do_POST(self) -> None:  # noqa: N802
                parsed = urllib.parse.urlparse(self.path)
                path = parsed.path.rstrip("/") or "/"
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b"{}"
                try:
                    doc = json.loads(raw or b"{}")
                except json.JSONDecodeError:
                    server.metrics["errors"] += 1
                    return self._json(-32700 if False else 400, {"error": "invalid JSON body"})

                if path == "/openc3-api/auth/verify":
                    pw = str(doc.get("password", ""))
                    if server.auth_required and pw != server.password:
                        server.metrics["errors"] += 1
                        return self._json(401, {"status": "error", "message": "invalid password"})
                    token = f"droid-sim-{uuid.uuid4().hex}"
                    server.tokens.add(token)
                    return self._json(
                        200, {"token": token, "expires_in": 3600, "scope": server.scope}
                    )

                if path in {"/openc3-api/api", "/__openc3_api__/api"}:
                    if not self._authorized():
                        return self._json(401, {"error": "unauthorized"})
                    rpc_id = doc.get("id")
                    method = doc.get("method", "")
                    params = doc.get("params") or []
                    keyword = doc.get("keyword_params") or {}
                    try:
                        result = server.dispatch(method, params, keyword)
                        return self._json(
                            200, {"jsonrpc": "2.0", "result": result, "id": rpc_id}
                        )
                    except _RpcFault as fault:
                        server.metrics["errors"] += 1
                        return self._json(
                            200,
                            {
                                "jsonrpc": "2.0",
                                "error": {"code": -32601, "message": str(fault)},
                                "id": rpc_id,
                            },
                        )
                    except Exception as exc:  # noqa: BLE001 - surfaced as RPC error
                        server.metrics["errors"] += 1
                        return self._json(
                            200,
                            {
                                "jsonrpc": "2.0",
                                "error": {"code": -32000, "message": f"{type(exc).__name__}: {exc}"},
                                "id": rpc_id,
                            },
                        )

                return self._json(404, {"error": f"no route {path}"})

        return Handler

    def _interface_loop(self) -> None:
        """Simulated spacecraft interface: writes packets into the CVT at rate."""
        interval = 1.0 / max(self.stream_rate_hz, 0.5)
        while not self._iface_stop.is_set():
            try:
                self.sim.tick()
            except Exception as exc:  # noqa: BLE001 - keep the interface alive
                self.metrics["errors"] += 1
                print(f"[droid-sim] interface error: {exc}", flush=True)
            self._iface_stop.wait(interval)

    def start(self) -> "MockCosmosServer":
        self._httpd = ThreadingHTTPServer((self.host, self.port), self._make_handler())
        self._httpd.daemon_threads = True
        self._thread = threading.Thread(target=self._httpd.serve_forever, daemon=True)
        self._thread.start()
        self._iface_stop.clear()
        self._iface = threading.Thread(target=self._interface_loop, daemon=True)
        self._iface.start()
        return self

    @property
    def url(self) -> str:
        host = "127.0.0.1" if self.host in {"0.0.0.0", ""} else self.host
        return f"http://{host}:{self.port}"

    def stop(self) -> None:
        self._iface_stop.set()
        if self._iface:
            self._iface.join(timeout=3)
        if self._httpd:
            self._httpd.shutdown()
            self._httpd.server_close()
        if self._thread:
            self._thread.join(timeout=5)

    def wait_ready(self, timeout: float = 20.0) -> bool:
        import urllib.request

        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(f"{self.url}/health", timeout=1.0) as r:
                    if r.status == 200:
                        return True
            except Exception:  # noqa: BLE001
                time.sleep(0.1)
        return False

    def __enter__(self) -> "MockCosmosServer":
        return self.start()

    def __exit__(self, *exc: object) -> None:
        self.stop()


class _RpcFault(Exception):
    pass


def _split_tlm_params(params: list[Any]) -> tuple[str, str, str]:
    if len(params) == 1:
        parts = str(params[0]).split()
        if len(parts) != 3:
            raise _RpcFault(f"invalid tlm parameter {params[0]!r}: expected 'TARGET PACKET ITEM'")
        return parts[0].upper(), parts[1].upper(), parts[2].upper()
    if len(params) >= 3:
        return str(params[0]).upper(), str(params[1]).upper(), str(params[2]).upper()
    raise _RpcFault(f"invalid tlm params {params!r}")


def _value_for(point: Any, value_type: str) -> Any:
    if value_type == "RAW":
        return point.dn
    if value_type == "FORMATTED":
        return point.formatted.split(" ")[0]
    return point.eu


def serve(**kwargs: Any) -> None:
    """Blocking entry point used by ``docker compose up`` and ``demo``."""
    import os

    server = MockCosmosServer(**kwargs)
    server.start()
    print(
        f"DROID_SIM OpenC3 COSMOS v5 reference server listening on "
        f"{server.host}:{server.port} (scope {server.scope})",
        flush=True,
    )
    print(f"  JSON-RPC : POST {server.url}/openc3-api/api", flush=True)
    print(f"  Auth     : POST {server.url}/openc3-api/auth/verify", flush=True)
    print(
        f"  Stream   : GET  {server.url}/openc3-api/stream?topic=DEFAULT__DECOM__{{DROID_SIM}}__EPS",
        flush=True,
    )
    duration = float(os.environ.get("COSMOS_SERVE_DURATION", "0"))
    try:
        if duration > 0:
            # `docker compose up` is a finite demo, not a daemon: stop after the
            # acceptance pass so Compose can return to the caller.
            time.sleep(duration)
        else:
            while True:
                time.sleep(3600)
    except KeyboardInterrupt:
        pass
    finally:
        server.stop()


if __name__ == "__main__":  # pragma: no cover
    serve(host=os.environ.get("COSMOS_HOST", "0.0.0.0") if (os := __import__("os")) else "0.0.0.0")
