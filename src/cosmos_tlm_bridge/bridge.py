"""The telemetry-to-microservice bridge.

Turns an OpenC3 JSON-RPC / StreamingApi telemetry stream into typed
``TelemetryFrame`` objects and forwards them to downstream Go / Python /
React ground microservices as JSON webhooks. No Ruby, no Script Runner.
"""

from __future__ import annotations

import json
import logging
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from typing import Any, Callable, Iterable

from .client import CosmosClient, CosmosError
from .models import TargetDefinition, ValueType
from .telemetry import TelemetryFrame, build_channel, channel_parts

__all__ = ["TelemetryBridge", "WebhookSink", "BridgeStats", "PointSubscription"]

log = logging.getLogger("cosmos_tlm_bridge.bridge")


@dataclass
class BridgeStats:
    frames: int = 0
    points: int = 0
    webhook_ok: int = 0
    webhook_failed: int = 0
    violations: int = 0
    last_frame_time: float = 0.0
    errors: list[str] = field(default_factory=list)

    def as_dict(self) -> dict[str, Any]:
        return {
            "frames": self.frames,
            "points": self.points,
            "webhook_ok": self.webhook_ok,
            "webhook_failed": self.webhook_failed,
            "violations": self.violations,
            "last_frame_time": self.last_frame_time,
            "errors": self.errors[-10:],
        }


@dataclass
class PointSubscription:
    """A single ``TARGET__PACKET__POINT`` subscription in DN or EU mode."""

    channel: str
    value_type: ValueType = ValueType.CONVERTED

    @property
    def target(self) -> str:
        return channel_parts(self.channel)[0]

    @property
    def packet(self) -> str:
        return channel_parts(self.channel)[1]

    @property
    def point(self) -> str:
        return channel_parts(self.channel)[2]

    @property
    def streaming_key(self) -> str:
        """OpenC3 StreamingApi item key:
        ``DECOM__TLM__TARGET__PACKET__ITEM__VALUETYPE__``.
        """
        return (
            f"DECOM__TLM__{self.target}__{self.packet}__{self.point}"
            f"__{self.value_type.value}__"
        )


class WebhookSink:
    """POSTs frames to a downstream microservice. Retries, never raises."""

    def __init__(
        self,
        url: str,
        *,
        timeout: float = 5.0,
        retries: int = 3,
        headers: dict[str, str] | None = None,
        dry_run: bool = False,
    ) -> None:
        self.url = url
        self.timeout = timeout
        self.retries = retries
        self.headers = {"Content-Type": "application/json", **(headers or {})}
        self.dry_run = dry_run

    def send(self, payload: dict[str, Any]) -> bool:
        if self.dry_run:
            log.debug("dry-run webhook %s <- %s", self.url, payload.get("topic"))
            return True
        body = json.dumps(payload).encode()
        for attempt in range(1, self.retries + 1):
            req = urllib.request.Request(self.url, data=body, method="POST", headers=self.headers)
            try:
                with urllib.request.urlopen(req, timeout=self.timeout) as resp:  # noqa: S310
                    if 200 <= resp.status < 300:
                        return True
                    log.warning("webhook %s returned HTTP %s", self.url, resp.status)
            except (urllib.error.URLError, OSError) as exc:
                log.warning(
                    "webhook %s attempt %d/%d failed: %s", self.url, attempt, self.retries, exc
                )
            time.sleep(0.2 * attempt)
        return False


class TelemetryBridge:
    """Decommutates a COSMOS stream into typed frames and fans them out.

    >>> bridge = TelemetryBridge(client, target_def)
    >>> bridge.on_frame(lambda f: print(f.target, f.packet, f.limits_overall))
    >>> bridge.run(["EPS", "THERMAL"], duration=5.0)
    """

    def __init__(
        self,
        client: CosmosClient,
        target_def: TargetDefinition,
        *,
        webhooks: Iterable[Any] = (),
        poll_hz: float = 4.0,
        scope: str | None = None,
    ) -> None:
        self.client = client
        self.target_def = target_def
        self.scope = (scope or client.scope).upper()
        self.poll_hz = poll_hz
        # Accept a URL string (wrapped in a WebhookSink) or any object that
        # implements `send(payload: dict) -> bool`, so tests and bespoke
        # publishers (Kafka, NATS, gRPC) can be plugged in.
        self.webhooks: list[Any] = [
            WebhookSink(w) if isinstance(w, str) else w for w in webhooks
        ]
        self.stats = BridgeStats()
        self._callbacks: list[Callable[[TelemetryFrame], None]] = []
        self._on_violation: list[Callable[[TelemetryFrame, dict[str, Any]], None]] = []
        self._stop = threading.Event()
        self.last_frames: dict[str, TelemetryFrame] = {}

    # -- wiring ------------------------------------------------------------ #

    def on_frame(self, cb: Callable[[TelemetryFrame], None]) -> None:
        self._callbacks.append(cb)

    def on_violation(self, cb: Callable[[TelemetryFrame, dict[str, Any]], None]) -> None:
        self._on_violation.append(cb)

    def stop(self) -> None:
        self._stop.set()

    # -- core loop --------------------------------------------------------- #

    def poll_once(self, packet: str) -> TelemetryFrame:
        """Read one packet via JSON-RPC and type it.

        Uses ``get_tlm_packet`` with ``type=RAW`` so the bridge performs the
        DN -> EU conversion itself from the target definition (identical maths
        to COSMOS), and then verifies its own EU result against a second
        ``type=CONVERTED`` read.
        """
        packet = packet.upper()
        dn_rows = self.client.get_tlm_packet(self.target_def.name, packet, "RAW")
        dn: dict[str, float] = {}
        cosmo_states: dict[str, str] = {}
        for name, value, state in dn_rows:
            dn[str(name).upper()] = float(value)
            cosmo_states[str(name).upper()] = str(state)

        frame = TelemetryFrame.from_dn(
            self.target_def,
            packet,
            dn,
            scope=self.scope,
            received_time=time.time(),
            received_count=int(self.client.get_tlm_cnt(self.target_def.name, packet) or 0),
        )
        # Cross-check: our evaluated limits state must agree with COSMOS'.
        for name, point in frame.points.items():
            server_state = cosmo_states.get(name)
            if server_state and server_state != point.limits_state.value:
                self.stats.errors.append(
                    f"limits divergence {point.channel}: bridge={point.limits_state.value} "
                    f"cosmos={server_state}"
                )
        self.last_frames[packet] = frame
        return frame

    def emit(self, frame: TelemetryFrame) -> None:
        self.stats.frames += 1
        self.stats.points += len(frame.points)
        self.stats.last_frame_time = frame.received_time
        summary = frame.limits_summary()
        if summary.violations:
            self.stats.violations += 1

        payload = frame.to_webhook_payload()
        for sink in self.webhooks:
            if sink.send(payload):
                self.stats.webhook_ok += 1
            else:
                self.stats.webhook_failed += 1

        for cb in self._callbacks:
            try:
                cb(frame)
            except Exception as exc:  # noqa: BLE001 - a bad callback must not kill the stream
                self.stats.errors.append(f"callback error: {exc}")

        if summary.violations:
            for cb in self._on_violation:
                try:
                    cb(frame, {"overall": summary.overall.value, "items": summary.violations})
                except Exception as exc:  # noqa: BLE001
                    self.stats.errors.append(f"violation callback error: {exc}")

    def run(
        self,
        packets: Iterable[str],
        *,
        duration: float = 5.0,
        max_frames: int | None = None,
    ) -> BridgeStats:
        """Poll ``packets`` at ``poll_hz`` until ``duration`` elapses."""
        packets = [p.upper() for p in packets]
        interval = 1.0 / max(self.poll_hz, 0.1)
        deadline = time.monotonic() + duration
        while not self._stop.is_set() and time.monotonic() < deadline:
            for packet in packets:
                try:
                    self.emit(self.poll_once(packet))
                except CosmosError as exc:
                    self.stats.errors.append(str(exc))
                if max_frames is not None and self.stats.frames >= max_frames:
                    return self.stats
                if self._stop.is_set():
                    break
            self._stop.wait(interval)
        return self.stats

    # -- adaptation -------------------------------------------------------- #

    def adapt(
        self,
        channel: str,
        value_type: ValueType | str = ValueType.CONVERTED,
        *,
        frame: TelemetryFrame | None = None,
    ) -> float:
        """Read one channel in DN or EU mode using the typed model."""
        vt = value_type if isinstance(value_type, ValueType) else ValueType.from_dn_eu(value_type)
        target, packet, point = (
            channel_parts(channel, self.target_def.name)
            if "__" in channel
            else (self.target_def.name, *channel.split("."))
        )
        if vt is ValueType.RAW:
            return float(self.client.tlm(target, packet, point, "RAW"))
        if vt is ValueType.CONVERTED:
            if frame is not None:
                return frame.point(point).eu
            return float(self.client.tlm(target, packet, point, "CONVERTED"))
        raise ValueError("FORMATTED channels are strings; use client.tlm_formatted()")

    def subscription_keys(self, subscriptions: Iterable[PointSubscription]) -> list[str]:
        """StreamingApi item keys, for wiring the real COSMOS WebSocket client."""
        return [s.streaming_key for s in subscriptions]

    def resolve_channels(self, channels: Iterable[str]) -> list[PointSubscription]:
        out: list[PointSubscription] = []
        for channel in channels:
            if ":" in channel:
                name, mode = channel.rsplit(":", 1)
                vt = ValueType.from_dn_eu(mode)
            else:
                name, vt = channel, ValueType.CONVERTED
            target, packet, point = channel_parts(name, self.target_def.name)
            self.target_def.item(packet, point)  # validate against the definition
            out.append(PointSubscription(build_channel(target, packet, point), vt))
        return out
