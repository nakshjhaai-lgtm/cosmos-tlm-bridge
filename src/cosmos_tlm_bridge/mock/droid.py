"""Deterministic DROID satellite simulator.

The simulator emits the raw DN (wire counts) for the OpenC3 ``DROID_SIM``
target. DN -> EU conversion and limits evaluation are done by the same code
path the bridge uses against a real COSMOS, so a passing acceptance run here
means the bridge logic is correct, not the mock.

Timeline (mission elapsed seconds, compressed for CI):

* ``0-4s``   — EPS/THERMAL/ADCS nominal, everything GREEN.
* ``4s``     — telemetry on EPS switches to the BLUE operational band
               (healthy bus voltage), proving the BLUE state is asserted.
* ``CMD_SLEW_TARGET`` — starts the maneuver: ATTITUDE_ERROR ramps up, then
  the reaction wheel bearing temperature climbs.
* ``+slew`` — ``THERMAL__RW_TEMP`` crosses YELLOW_HIGH (55 C) and is held
  there long enough to satisfy a persistence of 3 samples, then recovers.

Everything is seeded so runs are reproducible in CI.
"""

from __future__ import annotations

import math
import random
import threading
import time
from dataclasses import dataclass, field
from typing import Any

__all__ = ["DroidSimulator", "SlewCommand"]

C_OFFSET = -40.0
C_SCALE = 0.01
VOLT_OFFSET = 13.6
VOLT_SCALE = 0.0025


def _u16(value: float) -> int:
    return max(0, min(65535, int(round(value))))


def to_dn(eu: float, offset: float = C_OFFSET, scale: float = C_SCALE) -> int:
    """Encode an engineering value back into raw counts, clamped to uint16."""
    return max(0, min(65535, int(round((eu - offset) / scale))))


@dataclass
class SlewCommand:
    slew_rate: float = 2.0
    target_id: int = 1
    duration: int = 60
    abort_on_limits: int = 0
    issued_at: float = field(default_factory=time.monotonic)


class DroidSimulator:
    """Thread-safe state machine for one DROID satellite pass."""

    TARGET = "DROID_SIM"

    def __init__(self, seed: int = 1337, time_scale: float = 1.0) -> None:
        self._rng = random.Random(seed)
        self._lock = threading.RLock()
        self.time_scale = time_scale
        self.started_at = time.monotonic()
        self.sequence = {"ADCS": 0, "EPS": 0, "THERMAL": 0}
        self.commands: list[dict[str, Any]] = []
        self.slew: SlewCommand | None = None
        self.limits_set = "DEFAULT"
        self._rw_temp_eu = 21.0
        self._att_err_eu = 0.12
        self._soc = 91.5
        self._last_limits_state: dict[str, str] = {}
        self.limits_events: list[dict[str, Any]] = []
        self._last_tick = 0.0
        # The "current value table": what the interface last wrote per packet.
        self.latest: dict[str, dict[str, int]] = {}

    # -- state ------------------------------------------------------------- #

    @property
    def elapsed(self) -> float:
        return (time.monotonic() - self.started_at) * self.time_scale

    def send_command(self, command_string: str) -> dict[str, Any]:
        """Parse and apply a COSMOS command string."""
        text = " ".join(command_string.split())
        head = text.split("with")[0].strip().upper()
        tokens = head.split()
        if len(tokens) < 2 or tokens[0] != self.TARGET:
            raise ValueError(
                f"unknown command target in {command_string!r}: expected {self.TARGET} <CMD>"
            )
        name = tokens[1]
        args = self._parse_args(text)

        with self._lock:
            if name == "CMD_SLEW_TARGET":
                slew = SlewCommand(
                    slew_rate=float(args.get("SLEW_RATE", 2.0)),
                    target_id=int(args.get("TARGET_ID", 1)),
                    duration=int(args.get("DURATION", 60)),
                    abort_on_limits=int(args.get("ABORT_ON_LIMITS", 0)),
                )
                self.slew = slew
            elif name == "CMD_SET_LIMITS":
                self.limits_set = str(args.get("LIMITS_SET", "DEFAULT")).upper()
            self.commands.append(
                {"name": name, "args": args, "time": time.time(), "elapsed": self.elapsed}
            )
            return {
                "name": name,
                "args": args,
                "received_time": time.time(),
                "elapsed": round(self.elapsed, 3),
            }

    @staticmethod
    def _parse_args(text: str) -> dict[str, Any]:
        if " with " not in f" {text} ":
            return {}
        body = text.split("with", 1)[1]
        args: dict[str, Any] = {}
        for chunk in body.split(","):
            part = chunk.strip().split()
            if len(part) >= 2:
                key = part[0].upper()
                raw = part[1].strip('"')
                try:
                    args[key] = int(raw)
                except ValueError:
                    try:
                        args[key] = float(raw)
                    except ValueError:
                        args[key] = raw
        return args

    # -- physics ----------------------------------------------------------- #

    # Thermal/electrical time constants, in seconds of simulated time.
    TAU_HEAT = 1.5
    TAU_COOL = 3.0

    def tick(self) -> dict[str, dict[str, int]]:
        """One interface cycle: integrate state and publish all packets.

        Mirrors an OpenC3 interface thread writing packets into the current
        value table, so ``get_tlm_cnt`` advances even when nobody is reading.
        """
        with self._lock:
            t = self.elapsed
            dt = max(0.0, min(t - self._last_tick, 5.0))
            self._last_tick = t
            self._advance(dt)
            self._ensure_slew_window(t)
            for packet in ("ADCS", "EPS", "THERMAL"):
                self.latest[packet] = self._build_dn(packet, t)
            return dict(self.latest)

    def _advance(self, dt: float = 0.25) -> float:
        """Integrate the plant over ``dt`` seconds. Lock must be held."""
        t = self.elapsed

        # Reaction-wheel thermal model: baseline + slew heating with an
        # exponential rise toward a slew-rate dependent steady state, then a
        # passive cooldown once the slew completes (or is aborted).
        slew_temp = 24.0 + 14.0 * (self.slew.slew_rate if self.slew else 0.0)
        slew_temp = min(slew_temp, 78.0)  # YELLOW_HIGH=55 C, RED_HIGH=70 C
        heating = self.slew is not None and t < (self._slew_end or 0.0)
        if heating:
            frac = 1.0 - math.exp(-dt / self.TAU_HEAT)
            self._rw_temp_eu += (slew_temp - self._rw_temp_eu) * frac
            self._rw_temp_eu = min(self._rw_temp_eu, 79.0)
            self._att_err_eu += (0.9 + 0.5 * math.sin(t / 3.0) - self._att_err_eu) * frac
        else:
            frac = 1.0 - math.exp(-dt / self.TAU_COOL)
            self._rw_temp_eu += (21.0 - self._rw_temp_eu) * frac
            self._att_err_eu += (0.12 - self._att_err_eu) * frac

        # Autonomous abort protection: ETR-style behaviour the acceptance suite
        # asserts against.
        if (
            self.slew
            and self.slew.abort_on_limits
            and self._rw_temp_eu >= 55.0
            and self._slew_end
        ):
            self._slew_end = min(self._slew_end, t + 0.5)

        self._rw_temp_eu += self._rng.uniform(-0.08, 0.08) * min(dt, 1.0)
        self._att_err_eu = max(0.0, self._att_err_eu + self._rng.uniform(-0.02, 0.02) * min(dt, 1.0))

        # Battery: a slow sag while slewing, recharged by the arrays.
        drain = 0.06 if heating else 0.015
        self._soc = max(38.0, min(99.0, self._soc - drain / 10.0))
        return t

    _slew_end: float | None = None

    def _ensure_slew_window(self, t: float) -> None:
        if self.slew is not None and self._slew_end is None:
            self._slew_end = t + max(6.0, self.slew.duration * 0.35)

    # -- packet generation -------------------------------------------------- #

    def _ccsds(self, packet: str, stream_id: int, body: dict[str, int]) -> dict[str, int]:
        self.sequence[packet] += 1
        now = time.time()
        seconds = int(now)
        subseconds = int((now - seconds) * (2**32))
        return {
            "CCSDS_STREAMID": stream_id,
            "CCSDS_SEQUENCE": self.sequence[packet] & 0x3FFF,
            "CCSDS_LENGTH": 10 + len(body),
            "CCSDS_SECONDS": seconds,
            "CCSDS_SUBSECONDS": subseconds,
            **body,
        }

    def packet_dn(self, packet: str) -> dict[str, int]:
        """Raw DN from the current value table (ticks the plant if empty)."""
        packet = packet.upper()
        with self._lock:
            if packet not in self.latest:
                self.tick()
            if packet not in self.latest:
                raise ValueError(f"unknown DROID_SIM packet {packet!r}")
            return dict(self.latest[packet])

    def _build_dn(self, packet: str, t: float) -> dict[str, int]:
        """Encode the current plant state into one packet's raw DN."""
        packet = packet.upper()
        slewing = self.slew is not None and t < (self._slew_end or 0.0)

        if packet == "ADCS":
            body = {
                "ATTITUDE_ERROR": to_dn(self._att_err_eu, 0.0, 0.001),
                "SLEW_ACTIVE": 1 if slewing else 0,
                "SLEW_TARGET_ID": self.slew.target_id if (slewing and self.slew) else 0,
                "RW_SPEED": _u16(
                    (1200.0 + 320.0 * math.sin(t / 4.0)) / 0.1
                    if slewing
                    else (300.0 + 40.0 * math.sin(t / 9.0)) / 0.1
                ),
            }
            return self._ccsds("ADCS", 0x08AD, body)

        if packet == "EPS":
            volts = 15.55 + 0.18 * math.sin(t / 7.0) - (0.30 if slewing else 0.0)
            amps = -1.2 + (0.9 if slewing else 0.0) + 0.15 * math.sin(t / 5.0)
            body = {
                "BATT_VOLTAGE": to_dn(volts, VOLT_OFFSET, VOLT_SCALE),
                "BATT_CURRENT": to_dn(amps, -8.0, 0.01),
                "BATT_SOC": to_dn(self._soc, 0.0, 0.01),
                "BATT_TEMP": to_dn(
                    18.0 + 1.5 * math.sin(t / 11.0) + (2.0 if slewing else 0.0)
                ),
            }
            return self._ccsds("EPS", 0x08E1, body)

        if packet == "THERMAL":
            radiator = 12.0 + 3.0 * math.sin(t / 13.0) + (4.5 if slewing else 0.0)
            bus = 8.0 + 2.0 * math.sin(t / 17.0) + (1.0 if slewing else 0.0)
            heater = max(0.0, min(60.0, 8.0 + 14.0 * (1.0 - self._rw_temp_eu / 40.0)))
            body = {
                "RADIATOR_TEMP": to_dn(radiator),
                "RW_TEMP": to_dn(self._rw_temp_eu),
                "BUS_TEMP": to_dn(bus),
                "HEATER_DUTY": to_dn(heater, 0.0, 0.01),
            }
            return self._ccsds("THERMAL", 0x08CC, body)

        raise ValueError(f"unknown DROID_SIM packet {packet!r}")

    @property
    def limiting_point(self) -> tuple[str, float]:
        """The point most likely to alarm, used by early-warning assertions."""
        return "THERMAL__RW_TEMP", self._rw_temp_eu

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            return {
                "target": self.TARGET,
                "elapsed": round(self.elapsed, 3),
                "limits_set": self.limits_set,
                "slew": None
                if self.slew is None
                else {
                    "slew_rate": self.slew.slew_rate,
                    "target_id": self.slew.target_id,
                    "duration": self.slew.duration,
                    "abort_on_limits": self.slew.abort_on_limits,
                },
                "rw_temp_eu": round(self._rw_temp_eu, 3),
                "attitude_error_eu": round(self._att_err_eu, 4),
                "batt_soc": round(self._soc, 2),
                "commands": len(self.commands),
            }

    def record_limits_events(self, packet: str, states: dict[str, str]) -> list[dict[str, Any]]:
        """Diff limits states to emit COSMOS-style limits-change events."""
        events: list[dict[str, Any]] = []
        with self._lock:
            for item, state in states.items():
                key = f"{packet}__{item}"
                prev = self._last_limits_state.get(key)
                if prev is not None and prev != state:
                    event = {
                        "type": "LIMITS_CHANGE",
                        "target": self.TARGET,
                        "packet": packet,
                        "item": item,
                        "old_state": prev,
                        "new_state": state,
                        "time": time.time(),
                    }
                    events.append(event)
                    self.limits_events.append(event)
                self._last_limits_state[key] = state
        return events
