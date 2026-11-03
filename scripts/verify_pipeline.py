#!/usr/bin/env python3
"""Prove the end-to-end typed bridge without Ruby, credentials or a cloud SDK.

Boots the DROID_SIM OpenC3 v5 reference server and a downstream webhook sink,
reads all three required telemetry channels, sends CMD_SLEW_TARGET, catches the
reaction-wheel YELLOW_HIGH violation in DN/EU, and verifies webhook fan-out.

Exit 0 only when every assertion passes. No Docker or Go toolchain is needed.
"""
from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
os.environ["COSMOS_DEFINITIONS_DIR"] = str(ROOT / "definitions" / "droid_sim")

from cosmos_tlm_bridge.bridge import TelemetryBridge, WebhookSink  # noqa: E402
from cosmos_tlm_bridge.client import CosmosClient  # noqa: E402
from cosmos_tlm_bridge.mock.server import MockCosmosServer  # noqa: E402
from cosmos_tlm_bridge.models import load_target  # noqa: E402


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def wait_http(url: str, timeout: float = 10.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            urllib.request.urlopen(url, timeout=0.3).read()
            return
        except Exception:
            time.sleep(0.1)
    raise RuntimeError(f"service did not become ready: {url}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--duration", type=float, default=12.0)
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    cosmos_port, sink_port = free_port(), free_port()
    os.environ["COSMOS_PORT"] = str(cosmos_port)
    server = MockCosmosServer(host="127.0.0.1", port=cosmos_port, scope="DEFAULT", seed=1337)
    server.start()
    assert server.wait_ready(10), "DROID_SIM server did not start"
    sink = subprocess.Popen(
        [sys.executable, "-u", str(ROOT / "docker" / "sink.py")],
        env=dict(os.environ, SINK_PORT=str(sink_port), SINK_DURATION="0"),
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    sink_url = f"http://127.0.0.1:{sink_port}"
    wait_http(sink_url + "/health")

    client = CosmosClient(server.url, scope="DEFAULT")
    target = load_target(ROOT / "definitions" / "droid_sim", "DROID_SIM")
    channels = [
        ("ADCS", "ATTITUDE_ERROR"),
        ("EPS", "BATT_VOLTAGE"),
        ("THERMAL", "RADIATOR_TEMP"),
    ]
    observations = []
    for packet, item in channels:
        rows = client.get_tlm_packet("DROID_SIM", packet, "CONVERTED")
        values = {str(r[0]).upper(): (r[1], r[2]) for r in rows}
        eu, state = values[item]
        dn = client.rpc("tlm_raw", ["DROID_SIM", packet, item])
        channel = f"DROID_SIM__{packet}__{item}"
        observations.append({"channel": channel, "dn": dn, "eu": eu, "state": state})
        print(f"  {channel:<40} DN={dn:<6} EU={float(eu):<8.3f} state={state}")

    command = "DROID_SIM CMD_SLEW_TARGET with SLEW_RATE 2.5, TARGET_ID 7, DURATION 60, ABORT_ON_LIMITS 0"
    ack = client.cmd(command)
    print(f"  CMD_SLEW_TARGET acknowledged: {ack['name']} target={ack['args']['TARGET_ID']}")

    bridge = TelemetryBridge(
        client,
        target,
        webhooks=[WebhookSink(sink_url + "/tlm")],
        poll_hz=4.0,
    )
    violations: list[dict] = []

    def on_violation(frame, _event) -> None:
        for point in frame.points.values():
            if point.out_of_limits and point.limits_state.value in {"YELLOW_HIGH", "YELLOW_LOW", "RED_HIGH", "RED_LOW"}:
                violations.append({
                    "channel": point.channel,
                    "dn": point.dn,
                    "eu": point.eu,
                    "state": point.limits_state.value,
                })

    bridge.on_violation(on_violation)
    stats = bridge.run(["ADCS", "EPS", "THERMAL"], duration=args.duration)
    sink_state = json.load(urllib.request.urlopen(sink_url + "/health", timeout=2))

    # Required OpenC3 surfaces.
    auth_req = urllib.request.Request(
        server.url + "/openc3-api/auth/verify",
        data=b'{"password":"cosmos"}',
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    auth = json.load(urllib.request.urlopen(auth_req, timeout=2))
    endpoints = {}
    for path in ("/__openc3_api__/api", "/openc3-api/api"):
        body = json.dumps({
            "jsonrpc": "2.0", "id": 1, "method": "get_server_status",
            "params": [], "keyword_params": {"scope": "DEFAULT"},
        }).encode()
        req = urllib.request.Request(
            server.url + path, data=body,
            headers={"Content-Type": "application/json"}, method="POST",
        )
        endpoints[path] = json.load(urllib.request.urlopen(req, timeout=2))["result"]["version"]
    with urllib.request.urlopen(
        server.url + "/openc3-api/stream?topic=DEFAULT__DECOM__{DROID_SIM}__EPS&limit=3&rate=10",
        timeout=5,
    ) as response:
        stream_lines = [json.loads(line) for line in response.read().decode().splitlines() if line.strip()]

    wheel = [v for v in violations if v["channel"] == "DROID_SIM__THERMAL__RW_TEMP" and v["state"] == "YELLOW_HIGH"]
    summary = {
        "channels": observations,
        "command": ack["name"],
        "endpoints": endpoints,
        "auth_verify_scope": auth["scope"],
        "stream_frames": len(stream_lines),
        "frames": stats.frames,
        "points": stats.points,
        "webhook_ok": stats.webhook_ok,
        "webhook_failed": stats.webhook_failed,
        "sink_frames": sink_state["frames"],
        "sink_last_overall": sink_state["last_overall"],
        "violations": len(violations),
        "yellow_high_rw_temp": len(wheel),
        "first_yellow_high": wheel[0] if wheel else None,
        "no_ruby": not any(ROOT.rglob("*.rb")),
    }
    checks = {
        "three_required_channels": len(observations) == 3 and all(o["dn"] is not None for o in observations),
        "command_acknowledged": summary["command"] == "CMD_SLEW_TARGET",
        "both_json_rpc_paths": set(endpoints) == {"/__openc3_api__/api", "/openc3-api/api"},
        "auth_verify": summary["auth_verify_scope"] == "DEFAULT",
        "typed_stream": summary["stream_frames"] == 3,
        "webhooks_delivered": stats.webhook_ok > 0 and stats.webhook_failed == 0 and sink_state["frames"] == stats.frames,
        "yellow_high_caught": bool(wheel),
        "no_ruby": summary["no_ruby"],
        "no_bridge_errors": not stats.errors,
    }
    summary["checks"] = checks
    summary["passed"] = all(checks.values())
    print(f"  bridge frames={stats.frames} points={stats.points} "
          f"webhook_ok={stats.webhook_ok} webhook_failed={stats.webhook_failed}")
    print(f"  sink frames={sink_state['frames']} last_overall={sink_state['last_overall']} "
          f"YELLOW_HIGH samples={len(wheel)}")
    if wheel:
        v = wheel[0]
        print(f"  !! YELLOW_HIGH {v['channel']} DN={v['dn']} EU={v['eu']:.2f} C")
    if args.json:
        print(json.dumps(summary, indent=2))
    print("PIPELINE " + ("PASS" if summary["passed"] else "FAIL"))

    bridge.stop()
    sink.terminate()
    try:
        sink.wait(timeout=5)
    except subprocess.TimeoutExpired:
        sink.kill()
    server.stop()
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
