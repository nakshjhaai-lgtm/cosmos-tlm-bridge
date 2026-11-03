"""cosmos-tlm-bridge — OpenC3 COSMOS v5 telemetry-to-microservice bridge.

Keyless, zero-OpenC3-SDK: decommutates COSMOS JSON-RPC / StreamingApi
telemetry into typed Pydantic and Go structs, evaluates DN and EU operational
limits (GREEN / YELLOW / RED / BLUE), and runs declarative YAML acceptance
suites as CI gates.
"""

from __future__ import annotations

__version__ = "1.0.0"

from .bridge import BridgeStats, PointSubscription, TelemetryBridge, WebhookSink
from .client import (
    CosmosAuthError,
    CosmosClient,
    CosmosConnectionError,
    CosmosError,
    CosmosRequestError,
    CosmosRPCError,
    CosmosTimeoutError,
)
from .models import (
    ItemDefinition,
    Limits,
    LimitsColor,
    LimitsState,
    PacketDefinition,
    PolyConversion,
    TargetDefinition,
    ValueType,
    load_target,
    parse_tlm_definition,
)
from .telemetry import LimitsSummary, TelemetryFrame, TelemetryPoint, build_channel, channel_parts

__all__ = [
    "__version__",
    "CosmosClient",
    "CosmosError",
    "CosmosAuthError",
    "CosmosConnectionError",
    "CosmosRequestError",
    "CosmosRPCError",
    "CosmosTimeoutError",
    "TelemetryBridge",
    "WebhookSink",
    "BridgeStats",
    "PointSubscription",
    "TelemetryFrame",
    "TelemetryPoint",
    "LimitsSummary",
    "TargetDefinition",
    "PacketDefinition",
    "ItemDefinition",
    "Limits",
    "LimitsState",
    "LimitsColor",
    "PolyConversion",
    "ValueType",
    "load_target",
    "parse_tlm_definition",
    "build_channel",
    "channel_parts",
]
