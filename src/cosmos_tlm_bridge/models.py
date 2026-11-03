"""Typed OpenC3 target definition model (DROID_SIM).

Parses a subset of the OpenC3 COSMOS v5 cmd/tlm definition language so the same
``.txt`` files that a real COSMOS TargetModel loads also drive the bridge's
DN/EU conversion and limits engine. That is the whole point: no Ruby, one
source of truth.
"""

from __future__ import annotations

import re
from enum import Enum
from pathlib import Path
from typing import Iterable

from pydantic import BaseModel, ConfigDict, Field, computed_field

__all__ = [
    "ValueType",
    "LimitsState",
    "Limits",
    "PolyConversion",
    "ItemDefinition",
    "PacketDefinition",
    "TargetDefinition",
    "parse_tlm_definition",
]

# COSMOS value types, exactly as the JSON-RPC `keyword_params.type` accepts them.
RAW = "RAW"
CONVERTED = "CONVERTED"
FORMATTED = "FORMATTED"


class ValueType(str, Enum):
    """OpenC3 telemetry value types.

    ingenium-lib-cosmos maps these onto the DN/EU vocabulary the Turion ops
    teams use: DN -> RAW, EU -> CONVERTED.
    """

    RAW = "RAW"
    CONVERTED = "CONVERTED"
    FORMATTED = "FORMATTED"

    @classmethod
    def from_dn_eu(cls, value: str) -> "ValueType":
        key = value.strip().upper()
        if key in {"DN", "RAW"}:
            return cls.RAW
        if key in {"EU", "CONVERTED"}:
            return cls.CONVERTED
        if key in {"FORMATTED", "FMT"}:
            return cls.FORMATTED
        raise ValueError(
            f"invalid dn_eu {value!r}: expected one of DN, EU (aliases RAW, CONVERTED)"
        )

    @property
    def dn_eu(self) -> str:
        return "DN" if self is ValueType.RAW else "EU"

    @property
    def cosmos_suffix(self) -> str:
        """Suffix used by ingenium-lib-cosmos telemetry adapters."""
        return "__RAW" if self is ValueType.RAW else "__CONVERTED"


class LimitsColor(str, Enum):
    """Roll-up colour. This is what COSMOS ``get_overall_limits_state`` returns."""

    GREEN = "GREEN"
    YELLOW = "YELLOW"
    RED = "RED"
    BLUE = "BLUE"


class LimitsState(str, Enum):
    """Granular COSMOS limits states.

    Derived from the COSMOS ``LIMITS`` keyword semantics:

    * ``value <= red_low``                      -> ``RED_LOW``
    * ``red_low < value <= yellow_low``          -> ``YELLOW_LOW``
    * ``yellow_high <= value < red_high``        -> ``YELLOW_HIGH``
    * ``value >= red_high``                     -> ``RED_HIGH``
    * ``green_low <= value <= green_high``       -> ``BLUE`` (operational band)
    * otherwise                                 -> ``GREEN``
    * no limits applied to the item             -> ``UNKNOWN``
    """

    RED_LOW = "RED_LOW"
    YELLOW_LOW = "YELLOW_LOW"
    GREEN = "GREEN"
    BLUE = "BLUE"
    YELLOW_HIGH = "YELLOW_HIGH"
    RED_HIGH = "RED_HIGH"
    UNKNOWN = "UNKNOWN"

    @property
    def color(self) -> LimitsColor:
        return _STATE_COLOR[self]

    @property
    def is_violation(self) -> bool:
        return self in {
            LimitsState.YELLOW_LOW,
            LimitsState.YELLOW_HIGH,
            LimitsState.RED_LOW,
            LimitsState.RED_HIGH,
        }

    @classmethod
    def from_token(cls, token: str) -> "LimitsState":
        """Accept ``YELLOW_HIGH``, ``yellow high``, ``YELLOW``, ``BLUE`` ..."""
        key = re.sub(r"[\s\-]+", "_", token.strip().upper())
        if key in {"YELLOW", "WARN", "WARNING"}:
            return cls.YELLOW_HIGH
        if key in {"RED", "ALARM", "CRITICAL"}:
            return cls.RED_HIGH
        if key in {"GREEN", "NOMINAL", "OK"}:
            return cls.GREEN
        if key in {"BLUE", "OPERATIONAL"}:
            return cls.BLUE
        if key in {"UNKNOWN", "NONE", "DISABLED"}:
            return cls.UNKNOWN
        return cls(key)


_STATE_COLOR: dict[LimitsState, LimitsColor] = {
    LimitsState.RED_LOW: LimitsColor.RED,
    LimitsState.YELLOW_LOW: LimitsColor.YELLOW,
    LimitsState.GREEN: LimitsColor.GREEN,
    LimitsState.BLUE: LimitsColor.BLUE,
    LimitsState.YELLOW_HIGH: LimitsColor.YELLOW,
    LimitsState.RED_HIGH: LimitsColor.RED,
    LimitsState.UNKNOWN: LimitsColor.GREEN,
}


class PolyConversion(BaseModel):
    """COSMOS ``POLY_READ_CONVERSION c0 c1 c2 c3``: ``EU = c0 + c1*x + c2*x^2 + c3*x^3``."""

    model_config = ConfigDict(frozen=True)

    c0: float = 0.0
    c1: float = 1.0
    c2: float = 0.0
    c3: float = 0.0

    def to_eu(self, dn: float) -> float:
        return self.c0 + self.c1 * dn + self.c2 * dn**2 + self.c3 * dn**3

    def to_dn(self, eu: float) -> float:
        """Inverse conversion (linear exact; quadratic/cubic via Newton iteration).

        Used to derive DN-domain limits from EU-domain limits so that DN-mode
        assertions compare against the identical physical threshold.
        """
        if self.c2 == 0.0 and self.c3 == 0.0:
            if self.c1 == 0.0:
                raise ZeroDivisionError("degenerate POLY conversion: c1 == 0")
            return (eu - self.c0) / self.c1
        x = (eu - self.c0) / (self.c1 or 1.0)
        for _ in range(60):
            f = self.to_eu(x) - eu
            d = self.c1 + 2 * self.c2 * x + 3 * self.c3 * x**2
            if d == 0.0:
                break
            step = f / d
            x -= step
            if abs(step) < 1e-12:
                break
        return x

    @property
    def is_identity(self) -> bool:
        return (self.c0, self.c1, self.c2, self.c3) == (0.0, 1.0, 0.0, 0.0)

    @property
    def is_monotonic_increasing(self) -> bool:
        return self.c1 > 0 and self.c2 >= 0 and self.c3 >= 0


class Limits(BaseModel):
    """A single COSMOS ``LIMITS`` declaration (EU domain, as COSMOS defines them)."""

    model_config = ConfigDict(frozen=True)

    limits_set: str = "DEFAULT"
    persistence: int = 1
    enabled: bool = True
    red_low: float | None = None
    yellow_low: float | None = None
    yellow_high: float | None = None
    red_high: float | None = None
    # GREEN_LOW/GREEN_HIGH are COSMOS' optional "operational" (BLUE) limits.
    green_low: float | None = None
    green_high: float | None = None

    @property
    def has_operational_band(self) -> bool:
        return self.green_low is not None and self.green_high is not None

    def evaluate(self, eu_value: float) -> LimitsState:
        """Evaluate COSMOS limits-state precedence.

        Note that the operational (BLUE) band narrows *inside* green and takes
        precedence over plain GREEN, but never over YELLOW/RED.
        """
        if not self.enabled:
            return LimitsState.UNKNOWN
        if self.red_high is not None and eu_value >= self.red_high:
            return LimitsState.RED_HIGH
        if self.red_low is not None and eu_value <= self.red_low:
            return LimitsState.RED_LOW
        if self.yellow_high is not None and eu_value >= self.yellow_high:
            return LimitsState.YELLOW_HIGH
        if self.yellow_low is not None and eu_value <= self.yellow_low:
            return LimitsState.YELLOW_LOW
        if self.has_operational_band and (
            self.green_low <= eu_value <= self.green_high  # type: ignore[operator]
        ):
            return LimitsState.BLUE
        return LimitsState.GREEN

    def threshold_eu(self, state: LimitsState) -> float | None:
        return {
            LimitsState.RED_LOW: self.red_low,
            LimitsState.YELLOW_LOW: self.yellow_low,
            LimitsState.YELLOW_HIGH: self.yellow_high,
            LimitsState.RED_HIGH: self.red_high,
            LimitsState.BLUE: self.green_high,
        }.get(state)


class ItemDefinition(BaseModel):
    model_config = ConfigDict(frozen=True)

    name: str
    bit_size: int
    data_type: str
    description: str = ""
    units: str = ""
    format_string: str | None = None
    conversion: PolyConversion = Field(default_factory=PolyConversion)
    limits: Limits | None = None
    states: dict[str, int] = Field(default_factory=dict)

    def convert_to_eu(self, raw: float) -> float:
        return self.conversion.to_eu(raw)

    def convert_to_dn(self, eu: float) -> int:
        return int(round(self.conversion.to_dn(eu)))

    def limits_in_dn(self) -> Limits | None:
        """Re-express the EU limits in the DN domain via inverse conversion."""
        if self.limits is None:
            return None
        conv = self.conversion
        if conv.is_identity:
            return self.limits

        def dn(v: float | None) -> float | None:
            return None if v is None else round(conv.to_dn(v), 6)

        return Limits(
            limits_set=self.limits.limits_set,
            persistence=self.limits.persistence,
            enabled=self.limits.enabled,
            red_low=dn(self.limits.red_low),
            yellow_low=dn(self.limits.yellow_low),
            yellow_high=dn(self.limits.yellow_high),
            red_high=dn(self.limits.red_high),
            green_low=dn(self.limits.green_low),
            green_high=dn(self.limits.green_high),
        )


class PacketDefinition(BaseModel):
    model_config = ConfigDict(frozen=True)

    target: str
    name: str
    endianness: str = "BIG_ENDIAN"
    description: str = ""
    items: dict[str, ItemDefinition] = Field(default_factory=dict)

    def item(self, name: str) -> ItemDefinition:
        try:
            return self.items[name.upper()]
        except KeyError:  # pragma: no cover - exercised by error paths
            raise KeyError(
                f"unknown telemetry point {self.target} {self.name} {name}; "
                f"known points: {', '.join(sorted(self.items))}"
            ) from None

    def channel_names(self) -> list[str]:
        """``TARGET__PACKET__POINT`` channels for every point in this packet."""
        return [f"{self.target}__{self.name}__{n}" for n in self.items]


class TargetDefinition(BaseModel):
    """All packets for one COSMOS target, keyed ``TARGET__PACKET``."""

    model_config = ConfigDict(frozen=True)

    name: str
    packets: dict[str, PacketDefinition] = Field(default_factory=dict)
    commands: dict[str, dict[str, ItemDefinition]] = Field(default_factory=dict)

    def packet(self, name: str) -> PacketDefinition:
        try:
            return self.packets[name.upper()]
        except KeyError:  # pragma: no cover
            raise KeyError(
                f"unknown packet {self.name} {name}; known: {', '.join(sorted(self.packets))}"
            ) from None

    def item(self, packet: str, item: str) -> ItemDefinition:
        return self.packet(packet).item(item)

    def all_channels(self) -> list[str]:
        out: list[str] = []
        for pkt in self.packets.values():
            out.extend(pkt.channel_names())
        return out


# --------------------------------------------------------------------------- #
# Definition parser
# --------------------------------------------------------------------------- #

_TLM_RE = re.compile(
    r'^TELEMETRY\s+(?P<target>\S+)\s+(?P<packet>\S+)\s+(?P<endian>\S+)\s*"?(?P<desc>[^"]*)"?\s*$'
)
_CMD_RE = re.compile(
    r'^COMMAND\s+(?P<target>\S+)\s+(?P<cmd>\S+)\s+(?P<endian>\S+)\s*"?(?P<desc>[^"]*)"?\s*$'
)
_ITEM_RE = re.compile(
    r"^(?P<kind>APPEND_ITEM|APPEND_ID_ITEM|APPEND_PARAMETER)\s+"
    r"(?P<name>\S+)\s+(?P<size>\d+)\s+(?P<dtype>\S+)\s*\"?(?P<desc>[^\"]*)\"?\s*$"
)
_LIMITS_RE = re.compile(r"^LIMITS\s+(?P<rest>.+)$")
_POLY_RE = re.compile(r"^POLY_READ_CONVERSION\s+(?P<rest>.+)$")
_UNITS_RE = re.compile(r"^UNITS\s+(?P<units>\S+)(?:\s+(?P<abbr>\S+))?\s*$")
_FMT_RE = re.compile(r"^FORMAT_STRING\s+\"(?P<fmt>[^\"]*)\"\s*$")
_STATE_RE = re.compile(r'^STATE\s+"?(?P<key>[^"\s]+)"?\s+(?P<value>-?\d+)(?:\s+(?P<color>\S+))?\s*$')


def _parse_limits(rest: str) -> Limits:
    tokens = rest.split()
    limits_set = tokens[0]
    persistence = int(tokens[1]) if len(tokens) > 1 and tokens[1].isdigit() else 1
    enabled = True
    idx = 2
    if idx < len(tokens) and tokens[idx].upper() in {"ENABLED", "DISABLED"}:
        enabled = tokens[idx].upper() == "ENABLED"
        idx += 1
    nums = [float(t) for t in tokens[idx:]]
    # RED_LOW YELLOW_LOW YELLOW_HIGH RED_HIGH [GREEN_LOW GREEN_HIGH]
    while len(nums) < 4:
        nums.append(float("nan"))
    red_low, yellow_low, yellow_high, red_high = nums[:4]
    green_low = nums[4] if len(nums) > 4 else None
    green_high = nums[5] if len(nums) > 5 else None

    def clean(v: float | None) -> float | None:
        return None if v is None or v != v else v

    return Limits(
        limits_set=limits_set,
        persistence=persistence,
        enabled=enabled,
        red_low=clean(red_low),
        yellow_low=clean(yellow_low),
        yellow_high=clean(yellow_high),
        red_high=clean(red_high),
        green_low=clean(green_low),
        green_high=clean(green_high),
    )


def parse_tlm_definition(path: str | Path) -> dict[str, TargetDefinition]:
    """Parse OpenC3 ``*_tlm.txt`` / ``*_cmds.txt`` files into typed definitions."""
    path = Path(path)
    targets: dict[str, dict] = {}

    for line in path.read_text(encoding="utf-8").splitlines():
        raw = line.split("#", 1)[0].rstrip()
        if not raw.strip():
            continue
        stripped = raw.strip()

        m = _TLM_RE.match(stripped)
        if m:
            tgt = targets.setdefault(
                m["target"], {"name": m["target"], "packets": {}, "commands": {}}
            )
            tgt["packets"][m["packet"].upper()] = PacketDefinition(
                target=m["target"],
                name=m["packet"].upper(),
                endianness=m["endian"],
                description=m["desc"].strip(),
            ).model_dump()
            current = ("packet", m["target"], m["packet"].upper())
            continue

        m = _CMD_RE.match(stripped)
        if m:
            tgt = targets.setdefault(
                m["target"], {"name": m["target"], "packets": {}, "commands": {}}
            )
            tgt["commands"].setdefault(m["cmd"].upper(), {})
            current = ("command", m["target"], m["cmd"].upper())
            continue

        if "current" not in locals():
            continue

        m = _ITEM_RE.match(stripped)
        if m:
            item = ItemDefinition(
                name=m["name"].upper(),
                bit_size=int(m["size"]),
                data_type=m["dtype"].upper(),
                description=m["desc"].strip(),
            )
            kind, tgt_name, member = current
            if kind == "packet":
                pkt: dict = targets[tgt_name]["packets"][member]
                pkt["items"][item.name] = item.model_dump()
            else:
                targets[tgt_name]["commands"][member][item.name] = item.model_dump()
            continue

        kind, tgt_name, member = current
        if kind != "packet":
            continue
        pkt = targets[tgt_name]["packets"][member]
        item_name = next(reversed(pkt["items"]), None)
        if item_name is None:
            continue
        item = pkt["items"][item_name]

        if m := _LIMITS_RE.match(stripped):
            item["limits"] = _parse_limits(m["rest"]).model_dump()
        elif m := _POLY_RE.match(stripped):
            coeffs = [float(x) for x in m["rest"].split()][:4]
            while len(coeffs) < 4:
                coeffs.append(0.0)
            item["conversion"] = PolyConversion(
                c0=coeffs[0], c1=coeffs[1], c2=coeffs[2], c3=coeffs[3]
            ).model_dump()
        elif m := _UNITS_RE.match(stripped):
            item["units"] = (m["abbr"] or m["units"]).strip()
        elif m := _FMT_RE.match(stripped):
            item["format_string"] = m["fmt"]
        elif m := _STATE_RE.match(stripped):
            item["states"][m["key"].upper()] = int(m["value"])

    return {name: TargetDefinition.model_validate(data) for name, data in targets.items()}


def load_target(definitions_dir: str | Path, target: str) -> TargetDefinition:
    """Load and merge every definition file for ``target`` under ``definitions_dir``."""
    definitions_dir = Path(definitions_dir)
    files: Iterable[Path] = sorted(definitions_dir.rglob(f"{target.lower()}*_tlm.txt"))
    cmd_files = sorted(definitions_dir.rglob(f"{target.lower()}*_cmds.txt"))
    if not files:
        raise FileNotFoundError(
            f"no telemetry definition for target {target} under {definitions_dir}"
        )
    merged = TargetDefinition(name=target.upper())
    for f in [*files, *cmd_files]:
        for tname, tdef in parse_tlm_definition(f).items():
            if tname.upper() != target.upper():
                continue
            merged = TargetDefinition(
                name=tname.upper(),
                packets={**merged.packets, **tdef.packets},
                commands={**merged.commands, **tdef.commands},
            )
    return merged
