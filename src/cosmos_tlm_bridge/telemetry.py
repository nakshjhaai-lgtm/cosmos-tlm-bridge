"""Typed telemetry frames — the wire contract between COSMOS and Turion's
Go / Python / React ground microservices.

A ``TelemetryFrame`` is the decommutated form of one OpenC3 ``SCOPE__DECOM__``
packet: every telemetry point carries its raw DN, its converted EU, the
COSMOS limits state, and a normalised colour that downstream services can act
on without re-implementing the COSMOS limits algorithm.
"""

from __future__ import annotations

import time
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, computed_field

from .models import (
    ItemDefinition,
    LimitsColor,
    LimitsState,
    PacketDefinition,
    TargetDefinition,
    ValueType,
)

__all__ = [
    "TelemetryPoint",
    "TelemetryFrame",
    "LimitsSummary",
    "channel_parts",
    "build_channel",
]


def channel_parts(channel: str, default_target: str | None = None) -> tuple[str, str, str]:
    """Split an ingenium-style channel name into ``(target, packet, point)``.

    Accepts the canonical three-part ``TARGET__PACKET__POINT`` form and the
    two-part ``PACKET__POINT`` shorthand used throughout the Turion runbooks.
    Exactly two ``__`` delimiters are required for the canonical form.
    """
    parts = [p for p in channel.strip().upper().split("__")]
    if len(parts) == 3 and all(parts):
        return parts[0], parts[1], parts[2]
    if len(parts) == 2 and all(parts) and default_target:
        return default_target.upper(), parts[0], parts[1]
    raise ValueError(
        f"invalid telemetry channel {channel!r}: expected TARGET__PACKET__POINT "
        f"(or PACKET__POINT with a configured default target)"
    )


def build_channel(target: str, packet: str, point: str) -> str:
    return f"{target.upper()}__{packet.upper()}__{point.upper()}"


class TelemetryPoint(BaseModel):
    """One decommutated telemetry point in both DN and EU representations."""

    model_config = ConfigDict(frozen=True)

    channel: str = Field(description="TARGET__PACKET__POINT")
    target: str
    packet: str
    name: str
    dn: int | float
    eu: float
    formatted: str
    units: str = ""
    limits_state: LimitsState = LimitsState.UNKNOWN
    limits_color: LimitsColor = LimitsColor.GREEN
    limits_set: str = "DEFAULT"
    limits: dict[str, float | None] | None = None
    out_of_limits: bool = False

    @classmethod
    def from_definition(
        cls,
        definition: ItemDefinition,
        target: str,
        packet: str,
        dn_value: float,
        limits_set: str = "DEFAULT",
    ) -> "TelemetryPoint":
        eu = definition.convert_to_eu(dn_value)
        state = (
            definition.limits.evaluate(eu)
            if definition.limits is not None
            else LimitsState.UNKNOWN
        )
        if definition.format_string:
            try:
                formatted = f"{definition.format_string % eu} {definition.units}".strip()
            except (TypeError, ValueError):  # pragma: no cover - defensive
                formatted = f"{eu:g} {definition.units}".strip()
        else:
            formatted = f"{eu:g} {definition.units}".strip()
        return cls(
            channel=build_channel(target, packet, definition.name),
            target=target.upper(),
            packet=packet.upper(),
            name=definition.name,
            dn=dn_value if isinstance(dn_value, int) else round(dn_value, 6),
            eu=round(eu, 6),
            formatted=formatted,
            units=definition.units,
            limits_state=state,
            limits_color=state.color,
            limits_set=limits_set,
            limits=definition.limits.model_dump(
                include={
                    "red_low",
                    "yellow_low",
                    "yellow_high",
                    "red_high",
                    "green_low",
                    "green_high",
                }
            )
            if definition.limits
            else None,
            out_of_limits=state.is_violation,
        )

    def value_for(self, value_type: ValueType) -> float:
        if value_type is ValueType.RAW:
            return float(self.dn)
        if value_type is ValueType.CONVERTED:
            return self.eu
        raise ValueError(
            "FORMATTED has no numeric value; use .formatted / .value_for_numeric"
        )

    def represent(self, value_type: ValueType) -> float | str:
        return self.formatted if value_type is ValueType.FORMATTED else self.value_for(value_type)


class LimitsSummary(BaseModel):
    """Aggregate limits roll-up, mirroring COSMOS ``get_overall_limits_state``."""

    model_config = ConfigDict(frozen=True)

    overall: LimitsColor = LimitsColor.GREEN
    counts: dict[str, int] = Field(default_factory=dict)
    violations: list[str] = Field(default_factory=list)

    @classmethod
    def from_points(cls, points: list[TelemetryPoint]) -> "LimitsSummary":
        order = {LimitsColor.GREEN: 0, LimitsColor.BLUE: 1, LimitsColor.YELLOW: 2, LimitsColor.RED: 3}
        counts: dict[str, int] = {}
        worst = LimitsColor.GREEN
        violations: list[str] = []
        for p in points:
            counts[p.limits_state.value] = counts.get(p.limits_state.value, 0) + 1
            counts[f"COLOR_{p.limits_color.value}"] = (
                counts.get(f"COLOR_{p.limits_color.value}", 0) + 1
            )
            if order[p.limits_color] > order[worst]:
                worst = p.limits_color
            if p.out_of_limits:
                violations.append(f"{p.channel}={p.limits_state.value}")
        return cls(overall=worst, counts=counts, violations=sorted(violations))


class TelemetryFrame(BaseModel):
    """A fully typed, decommutated telemetry packet."""

    model_config = ConfigDict(frozen=True)

    schema_version: str = "1.0"
    scope: str = "DEFAULT"
    target: str
    packet: str
    topic: str = Field(description="OpenC3 stream topic SCOPE__DECOM__TARGET__PACKET")
    received_time: float = Field(default_factory=time.time)
    packet_time: str = ""
    received_count: int = 0
    points: dict[str, TelemetryPoint] = Field(default_factory=dict)
    limits_set: str = "DEFAULT"

    @computed_field  # type: ignore[prop-decorator]
    @property
    def limits_overall(self) -> str:
        return self.limits_summary().overall.value

    def limits_summary(self) -> LimitsSummary:
        return LimitsSummary.from_points(list(self.points.values()))

    def point(self, name: str) -> TelemetryPoint:
        key = name.upper()
        if key in self.points:
            return self.points[key]
        # allow "PACKET__POINT" or "TARGET__PACKET__POINT"
        if "__" in key:
            parts = key.split("__")
            if len(parts) == 3 and parts[0] == self.target:
                key = parts[2]
            elif len(parts) == 2:
                key = parts[1]
        try:
            return self.points[key]
        except KeyError:
            raise KeyError(
                f"packet {self.target} {self.packet} has no point {name!r}; "
                f"available: {', '.join(sorted(self.points))}"
            ) from None

    @classmethod
    def from_dn(
        cls,
        target_def: TargetDefinition,
        packet_name: str,
        dn: dict[str, float],
        *,
        scope: str = "DEFAULT",
        received_time: float | None = None,
        packet_time: str = "",
        received_count: int = 0,
        limits_set: str = "DEFAULT",
    ) -> "TelemetryFrame":
        packet: PacketDefinition = target_def.packet(packet_name)
        points: dict[str, TelemetryPoint] = {}
        for item_name, item_def in packet.items.items():
            if item_name not in dn:
                continue
            point = TelemetryPoint.from_definition(
                item_def, target_def.name, packet.name, dn[item_name], limits_set
            )
            points[item_name] = point
        ts = time.time() if received_time is None else received_time
        return cls(
            scope=scope,
            target=target_def.name,
            packet=packet.name,
            topic=f"{scope}__DECOM__{{{target_def.name}}}__{packet.name}",
            received_time=ts,
            packet_time=packet_time or time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(ts)),
            received_count=received_count,
            points=points,
            limits_set=limits_set,
        )

    def cosmos_overall_limits_state(self) -> str:
        """COSMOS parity: ``get_overall_limits_state`` only returns GREEN/YELLOW/RED.

        BLUE is an operational band (healthy), so it collapses to GREEN for
        consumers that expect the three-state COSMOS contract.
        """
        overall = self.limits_summary().overall
        return "GREEN" if overall is LimitsColor.BLUE else overall.value

    def to_webhook_payload(self) -> dict[str, Any]:
        """Flat, microservice-friendly projection (Go/React consume this shape)."""
        return {
            "schema_version": self.schema_version,
            "scope": self.scope,
            "target": self.target,
            "packet": self.packet,
            "topic": self.topic,
            "packet_time": self.packet_time,
            "received_count": self.received_count,
            "limits_overall": self.limits_overall,
            "cosmos_overall_limits_state": self.cosmos_overall_limits_state(),
            "limits_set": self.limits_set,
            "points": {
                name: {
                    "channel": p.channel,
                    "dn": p.dn,
                    "eu": p.eu,
                    "units": p.units,
                    "formatted": p.formatted,
                    "limits_state": p.limits_state.value,
                    "limits_color": p.limits_color.value,
                }
                for name, p in sorted(self.points.items())
            },
        }
