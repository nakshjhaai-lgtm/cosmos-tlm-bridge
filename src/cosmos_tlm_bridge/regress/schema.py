"""Declarative YAML acceptance-suite schema for ``cosmos-regress``.

A suite replaces the brittle Ruby/Python scripts that today live in OpenC3
Script Runner. Every assertion names a ``TARGET__PACKET__POINT`` channel and a
DN or EU representation, so the same suite validates raw counts and converted
engineering units.
"""

from __future__ import annotations

import re
from enum import Enum
from typing import Any

import yaml
from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from ..models import LimitsColor, LimitsState, ValueType

__all__ = [
    "Comparison",
    "Condition",
    "AssertionSpec",
    "CommandStep",
    "WaitStep",
    "SuiteSpec",
    "load_suite",
    "load_suite_text",
]


class Comparison(str, Enum):
    EQUAL = "EQUAL"
    NOT_EQUAL = "NOT_EQUAL"
    GT = "GT"
    GTE = "GTE"
    LT = "LT"
    LTE = "LTE"
    BETWEEN = "BETWEEN"
    OUTSIDE = "OUTSIDE"
    CHANGE = "CHANGE"
    TOLERANCE = "TOLERANCE"


class Condition(BaseModel):
    """A value predicate, parsed from ``"BETWEEN,0.0,1.0,,"`` style strings.

    Accepts the ingenium ``verification_condition`` CSV form and a friendlier
    operator form (``">= 0.5"``, ``"in 1..3"``).
    """

    model_config = ConfigDict(frozen=True)

    comparison: Comparison
    operands: tuple[float, ...] = ()
    tolerance: float = 0.0

    @classmethod
    def parse(cls, raw: str | dict[str, Any]) -> "Condition":
        if isinstance(raw, dict):
            return cls(
                comparison=Comparison(str(raw.get("comparison", raw.get("op", "EQUAL"))).upper()),
                operands=tuple(float(x) for x in raw.get("operands", raw.get("values", []))),
                tolerance=float(raw.get("tolerance", 0.0)),
            )
        text = raw.strip()
        # ingenium CSV form: COMPARISON,a,b,,
        if "," in text:
            parts = [p.strip() for p in text.split(",")]
            comp = Comparison(parts[0].upper())
            nums: list[float] = []
            for p in parts[1:]:
                if not p:
                    continue
                try:
                    nums.append(float(p))
                except ValueError:
                    continue
            return cls(comparison=comp, operands=tuple(nums))
        # operator form
        m = re.match(r"^(?P<op>>=|<=|==|!=|>|<|=|~)\s*(?P<v>-?[\d.eE+]+)$", text)
        if m:
            op = {
                ">=": "GTE",
                "<=": "LTE",
                ">": "GT",
                "<": "LT",
                "==": "EQUAL",
                "=": "EQUAL",
                "!=": "NOT_EQUAL",
                "~": "EQUAL",
            }[m["op"]]
            return cls(
                comparison=Comparison(op),
                operands=(float(m["v"]),),
                tolerance=0.0,
            )
        m = re.match(r"^(?P<a>-?[\d.eE+]+)\s*(?:\.\.|to)\s*(?P<b>-?[\d.eE+]+)$", text)
        if m:
            return cls(
                comparison=Comparison.BETWEEN,
                operands=(float(m["a"]), float(m["b"])),
            )
        raise ValueError(f"cannot parse comparison {raw!r}")

    def check(self, value: float, prior: float | None = None) -> tuple[bool, str]:
        c, o = self.comparison, self.operands
        if c is Comparison.EQUAL:
            ok = abs(value - o[0]) <= max(self.tolerance, 1e-9)
            return ok, f"{value} == {o[0]} (tol {self.tolerance})"
        if c is Comparison.NOT_EQUAL:
            return abs(value - o[0]) > max(self.tolerance, 1e-9), f"{value} != {o[0]}"
        if c is Comparison.GT:
            return value > o[0], f"{value} > {o[0]}"
        if c is Comparison.GTE:
            return value >= o[0], f"{value} >= {o[0]}"
        if c is Comparison.LT:
            return value < o[0], f"{value} < {o[0]}"
        if c is Comparison.LTE:
            return value <= o[0], f"{value} <= {o[0]}"
        if c is Comparison.BETWEEN:
            return o[0] <= value <= o[1], f"{o[0]} <= {value} <= {o[1]}"
        if c is Comparison.OUTSIDE:
            return not (o[0] <= value <= o[1]), f"{value} outside [{o[0]}, {o[1]}]"
        if c is Comparison.CHANGE:
            if prior is None:
                return False, "CHANGE requires a prior sample"
            return abs(value - prior) > max(o[0] if o else 0.0, 1e-12), (
                f"|{value} - {prior}| > {o[0] if o else 0}"
            )
        if c is Comparison.TOLERANCE:
            return abs(value - o[0]) <= (o[1] if len(o) > 1 else self.tolerance), (
                f"|{value} - {o[0]}| <= {o[1] if len(o) > 1 else self.tolerance}"
            )
        raise ValueError(f"unsupported comparison {c}")


class AssertionSpec(BaseModel):
    """One assertion against one channel."""

    model_config = ConfigDict(extra="forbid")

    channel: str = Field(description="TARGET__PACKET__POINT (PACKET__POINT allowed)")
    dn_eu: str = Field(default="EU", description="DN or EU")
    condition: Condition | None = None
    expect_limits: LimitsState | None = None
    expect_limits_color: LimitsColor | None = None
    within_s: float = Field(default=20.0, gt=0)
    description: str = ""
    occurrence: str = "ANY"  # ANY | EVER | ALWAYS
    sample_interval_s: float = 0.15

    @field_validator("dn_eu")
    @classmethod
    def _validate_dn_eu(cls, v: str) -> str:
        ValueType.from_dn_eu(v)  # raises on garbage
        return v.upper()

    @field_validator("condition", mode="before")
    @classmethod
    def _parse_condition(cls, v: Any) -> Any:
        if isinstance(v, str) or isinstance(v, dict):
            return Condition.parse(v)
        return v

    @field_validator("expect_limits", mode="before")
    @classmethod
    def _parse_limits(cls, v: Any) -> Any:
        return LimitsState.from_token(v) if isinstance(v, str) else v

    @field_validator("expect_limits_color", mode="before")
    @classmethod
    def _parse_color(cls, v: Any) -> Any:
        return LimitsColor(str(v).upper()) if isinstance(v, str) else v

    @model_validator(mode="after")
    def _require_predicate(self) -> "AssertionSpec":
        if self.condition is None and self.expect_limits is None and self.expect_limits_color is None:
            raise ValueError(
                f"assertion {self.channel!r} needs at least one of condition, "
                "expect_limits, expect_limits_color"
            )
        return self

    @property
    def value_type(self) -> ValueType:
        return ValueType.from_dn_eu(self.dn_eu)

    @property
    def label(self) -> str:
        bits = [f"{self.channel} [{self.dn_eu}]"]
        if self.condition:
            bits.append(self.condition.comparison.value)
        if self.expect_limits:
            bits.append(f"limits={self.expect_limits.value}")
        if self.expect_limits_color:
            bits.append(f"color={self.expect_limits_color.value}")
        return " ".join(bits)


class CommandStep(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str | None = None
    cmd: str
    check: str = "ENABLED"
    settle_s: float = 0.5

    @field_validator("check")
    @classmethod
    def _check(cls, v: str) -> str:
        key = v.upper()
        if key not in {"ENABLED", "NO_CHECK"}:
            raise ValueError("check must be ENABLED or NO_CHECK")
        return key


class AssertStep(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str | None = None
    assert_: list[AssertionSpec] = Field(alias="assert")
    require_all: bool = True

    @model_validator(mode="after")
    def _at_least_one(self) -> "AssertStep":
        if not self.assert_:
            raise ValueError("an assert step needs at least one assertion")
        return self

    @model_validator(mode="before")
    @classmethod
    def _coerce(cls, data: Any) -> Any:
        if isinstance(data, dict) and "assert" in data and isinstance(data["assert"], (dict, str)):
            data = {**data, "assert": [data["assert"]]}
        return data


class WaitStep(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str | None = None
    wait_s: float = Field(gt=0)
    reason: str = ""


class WaitForPacketStep(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str | None = None
    wait_for_packet: str
    within_s: float = Field(default=20.0, gt=0)


class WaitForLimitsStep(BaseModel):
    """Block until any point on a packet enters a limits state."""

    model_config = ConfigDict(extra="forbid")

    name: str | None = None
    wait_for_limits: LimitsState
    packet: str | None = None
    within_s: float = Field(default=30.0, gt=0)

    @field_validator("wait_for_limits", mode="before")
    @classmethod
    def _parse(cls, v: Any) -> Any:
        return LimitsState.from_token(v) if isinstance(v, str) else v


class SuiteSpec(BaseModel):
    """A full declarative acceptance suite."""

    model_config = ConfigDict(extra="forbid")

    suite: str
    description: str = ""
    target: str = "DROID_SIM"
    scope: str = "DEFAULT"
    timeout_s: float = Field(default=105.0, gt=0)
    tags: list[str] = Field(default_factory=list)
    fresh_sim: bool = Field(
        default=False,
        description="Reset the DROID_SIM plant before running (no-op on a real COSMOS)",
    )
    packets: list[str] = Field(default_factory=lambda: ["ADCS", "EPS", "THERMAL"])
    setup: list[dict[str, Any]] = Field(default_factory=list)
    steps: list[dict[str, Any]] = Field(default_factory=list)
    teardown: list[dict[str, Any]] = Field(default_factory=list)
    webhooks: list[str] = Field(default_factory=list)

    def normalised_steps(self) -> list[tuple[str, Any]]:
        out: list[tuple[str, Any]] = []
        for raw in self.steps:
            if "cmd" in raw:
                out.append(("cmd", CommandStep.model_validate(raw)))
            elif "assert" in raw:
                out.append(("assert", AssertStep.model_validate(raw)))
            elif "wait_s" in raw:
                out.append(("wait", WaitStep.model_validate(raw)))
            elif "wait_for_packet" in raw:
                out.append(("wait_for_packet", WaitForPacketStep.model_validate(raw)))
            elif "wait_for_limits" in raw:
                out.append(("wait_for_limits", WaitForLimitsStep.model_validate(raw)))
            else:
                raise ValueError(f"unrecognised step: {raw!r}")
        return out


def load_suite_text(text: str) -> SuiteSpec:
    doc = yaml.safe_load(text)
    if not isinstance(doc, dict):
        raise ValueError("suite YAML must be a mapping")
    return SuiteSpec.model_validate(doc)


def load_suite(path: str | Any) -> SuiteSpec:
    from pathlib import Path

    p = Path(path)
    if not p.is_file():
        raise FileNotFoundError(f"suite not found: {p}")
    return load_suite_text(p.read_text(encoding="utf-8"))
