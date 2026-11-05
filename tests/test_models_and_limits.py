"""Unit tests for the OpenC3 definition parser, DN/EU conversion, and the
COSMOS limits-state algorithm. These are the correctness core of the bridge:
everything else is transport.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from cosmos_tlm_bridge.models import (
    Limits,
    LimitsColor,
    LimitsState,
    PolyConversion,
    ValueType,
    parse_tlm_definition,
)
from cosmos_tlm_bridge.telemetry import (
    LimitsSummary,
    TelemetryFrame,
    TelemetryPoint,
    build_channel,
    channel_parts,
)

DEFS = Path(__file__).resolve().parents[1] / "definitions" / "droid_sim"


@pytest.fixture(scope="module")
def targets():
    return parse_tlm_definition(DEFS / "cmd_tlm" / "droid_sim_tlm.txt")


# --------------------------------------------------------------------------- #
# definition parsing
# --------------------------------------------------------------------------- #


def test_parses_three_packets(targets):
    droid = targets["DROID_SIM"]
    assert set(droid.packets) == {"ADCS", "EPS", "THERMAL"}
    assert len(droid.all_channels()) == 27


def test_parses_limits_in_cosmos_order(targets):
    lim = targets["DROID_SIM"].item("THERMAL", "RW_TEMP").limits
    assert lim is not None
    # LIMITS DEFAULT 3 ENABLED -20 0 55 70 10 45
    assert (lim.red_low, lim.yellow_low, lim.yellow_high, lim.red_high) == (-20, 0, 55, 70)
    assert (lim.green_low, lim.green_high) == (10, 45)
    assert lim.persistence == 3
    assert lim.enabled is True


def test_parses_poly_conversion_and_units(targets):
    item = targets["DROID_SIM"].item("EPS", "BATT_VOLTAGE")
    assert item.conversion == PolyConversion(c0=13.6, c1=0.0025)
    assert item.units == "V"
    assert item.format_string == "%0.3f"


def test_item_without_limits_is_unlimited(targets):
    assert targets["DROID_SIM"].item("THERMAL", "HEATER_DUTY").limits is None


def test_commands_are_parsed():
    cmds = parse_tlm_definition(DEFS / "cmd_tlm" / "droid_sim_cmds.txt")["DROID_SIM"]
    assert "CMD_SLEW_TARGET" in cmds.commands
    assert set(cmds.commands["CMD_SLEW_TARGET"]) >= {
        "SLEW_RATE",
        "TARGET_ID",
        "DURATION",
        "ABORT_ON_LIMITS",
    }


# --------------------------------------------------------------------------- #
# DN <-> EU conversion
# --------------------------------------------------------------------------- #


@pytest.mark.parametrize(
    ("dn", "eu"),
    [(0, 13.6), (400, 14.6), (800, 15.6), (1400, 17.1)],
)
def test_poly_forward_conversion(dn, eu):
    assert PolyConversion(c0=13.6, c1=0.0025).to_eu(dn) == pytest.approx(eu)


@pytest.mark.parametrize("eu", [-40.0, 0.0, 21.0, 55.0, 70.0])
def test_poly_inverse_round_trip(eu):
    conv = PolyConversion(c0=-40.0, c1=0.01)
    assert conv.to_eu(conv.to_dn(eu)) == pytest.approx(eu)


def test_inverse_handles_quadratic():
    conv = PolyConversion(c0=0.0, c1=0.5, c2=0.001)
    eu = conv.to_eu(250.0)
    assert conv.to_eu(conv.to_dn(eu)) == pytest.approx(eu, rel=1e-6)


def test_limits_are_rederived_in_the_dn_domain(targets):
    """The physical threshold must be identical in DN and EU modes."""
    item = targets["DROID_SIM"].item("THERMAL", "RW_TEMP")
    dn_limits = item.limits_in_dn()
    assert dn_limits is not None
    # yellow_high = 55 C, EU = -40 + 0.01*DN  ->  DN = 9500
    assert dn_limits.yellow_high == pytest.approx(9500.0)
    assert dn_limits.red_high == pytest.approx(11000.0)
    assert dn_limits.red_low == pytest.approx(2000.0)

    # The identical physical threshold must trip in both domains.
    for dn_value in (9500, 9560, 10999):
        eu = item.convert_to_eu(dn_value)
        assert item.limits.evaluate(eu) is LimitsState.YELLOW_HIGH
        assert dn_limits.evaluate(dn_value) is LimitsState.YELLOW_HIGH

    # One count below the threshold is still GREEN in both domains.
    assert item.limits.evaluate(item.convert_to_eu(9499)) is LimitsState.GREEN
    assert dn_limits.evaluate(9499) is LimitsState.GREEN


# --------------------------------------------------------------------------- #
# limits-state algorithm (COSMOS precedence)
# --------------------------------------------------------------------------- #


@pytest.fixture
def rw_limits() -> Limits:
    return Limits(
        red_low=-20,
        yellow_low=0,
        yellow_high=55,
        red_high=70,
        green_low=10,
        green_high=45,
        persistence=3,
    )


@pytest.mark.parametrize(
    ("eu", "state", "color"),
    [
        (-25.0, LimitsState.RED_LOW, LimitsColor.RED),
        (-20.0, LimitsState.RED_LOW, LimitsColor.RED),
        (-5.0, LimitsState.YELLOW_LOW, LimitsColor.YELLOW),
        (0.0, LimitsState.YELLOW_LOW, LimitsColor.YELLOW),
        (5.0, LimitsState.GREEN, LimitsColor.GREEN),
        (10.0, LimitsState.BLUE, LimitsColor.BLUE),
        (30.0, LimitsState.BLUE, LimitsColor.BLUE),
        (45.0, LimitsState.BLUE, LimitsColor.BLUE),
        (50.0, LimitsState.GREEN, LimitsColor.GREEN),
        (55.0, LimitsState.YELLOW_HIGH, LimitsColor.YELLOW),
        (69.9, LimitsState.YELLOW_HIGH, LimitsColor.YELLOW),
        (70.0, LimitsState.RED_HIGH, LimitsColor.RED),
        (99.0, LimitsState.RED_HIGH, LimitsColor.RED),
    ],
)
def test_limits_precedence(rw_limits, eu, state, color):
    assert rw_limits.evaluate(eu) is state
    assert state.color is color


def test_blue_band_never_masks_yellow_or_red(rw_limits):
    """A wider operational band must not swallow a yellow/red violation."""
    overlapping = rw_limits.model_copy(update={"green_low": 0, "green_high": 100})
    assert overlapping.evaluate(56.0) is LimitsState.YELLOW_HIGH
    assert overlapping.evaluate(75.0) is LimitsState.RED_HIGH


def test_disabled_limits_report_unknown(rw_limits):
    disabled = rw_limits.model_copy(update={"enabled": False})
    assert disabled.evaluate(1000.0) is LimitsState.UNKNOWN


def test_violation_flag(rw_limits):
    assert rw_limits.evaluate(60.0).is_violation is True
    assert rw_limits.evaluate(20.0).is_violation is False


# --------------------------------------------------------------------------- #
# channels and frames
# --------------------------------------------------------------------------- #


@pytest.mark.parametrize(
    ("channel", "expected"),
    [
        ("DROID_SIM__EPS__BATT_VOLTAGE", ("DROID_SIM", "EPS", "BATT_VOLTAGE")),
        ("EPS__BATT_VOLTAGE", ("DROID_SIM", "EPS", "BATT_VOLTAGE")),
    ],
)
def test_channel_parsing(channel, expected):
    assert channel_parts(channel, "DROID_SIM") == expected


@pytest.mark.parametrize("bad", ["", "EPS", "A__B__C__D", "EPS__"])
def test_channel_parsing_rejects_garbage(bad):
    with pytest.raises(ValueError):
        channel_parts(bad, "DROID_SIM")


def test_build_channel():
    assert build_channel("droid_sim", "eps", "batt_voltage") == "DROID_SIM__EPS__BATT_VOLTAGE"


def test_value_type_dn_eu_mapping():
    assert ValueType.from_dn_eu("DN") is ValueType.RAW
    assert ValueType.from_dn_eu("EU") is ValueType.CONVERTED
    assert ValueType.RAW.dn_eu == "DN"
    assert ValueType.CONVERTED.dn_eu == "EU"
    # ingenium-lib-cosmos suffix convention
    assert ValueType.RAW.cosmos_suffix == "__RAW"
    assert ValueType.CONVERTED.cosmos_suffix == "__CONVERTED"
    with pytest.raises(ValueError):
        ValueType.from_dn_eu("FURLONGS")


def test_frame_from_dn_roundtrip(targets):
    droid = targets["DROID_SIM"]
    frame = TelemetryFrame.from_dn(
        droid,
        "THERMAL",
        {"RADIATOR_TEMP": 5200, "RW_TEMP": 9560, "BUS_TEMP": 4800, "HEATER_DUTY": 1465},
    )
    rw = frame.point("RW_TEMP")
    assert rw.dn == 9560
    assert rw.eu == pytest.approx(55.6)
    assert rw.limits_state is LimitsState.YELLOW_HIGH
    assert rw.out_of_limits is True
    assert frame.limits_overall == "YELLOW"
    assert frame.topic == "DEFAULT__DECOM__{DROID_SIM}__THERMAL"
    payload = frame.to_webhook_payload()
    assert payload["cosmos_overall_limits_state"] == "YELLOW"
    assert payload["points"]["RW_TEMP"]["limits_state"] == "YELLOW_HIGH"


def test_frame_point_lookup_accepts_shorthand(targets):
    frame = TelemetryFrame.from_dn(targets["DROID_SIM"], "EPS", {"BATT_VOLTAGE": 800})
    assert frame.point("BATT_VOLTAGE").eu == pytest.approx(15.6)
    assert frame.point("EPS__BATT_VOLTAGE").eu == pytest.approx(15.6)
    assert frame.point("DROID_SIM__EPS__BATT_VOLTAGE").eu == pytest.approx(15.6)
    with pytest.raises(KeyError):
        frame.point("NOPE")


def test_limits_summary_rolls_up_worst_case(targets):
    frame = TelemetryFrame.from_dn(
        targets["DROID_SIM"],
        "THERMAL",
        {"RADIATOR_TEMP": 5200, "RW_TEMP": 11800, "BUS_TEMP": 4800, "HEATER_DUTY": 1465},
    )
    summary = frame.limits_summary()
    assert summary.overall is LimitsColor.RED
    assert any("RW_TEMP=RED_HIGH" in v for v in summary.violations)


def test_limits_summary_collapses_blue_for_cosmos_parity(targets):
    frame = TelemetryFrame.from_dn(
        targets["DROID_SIM"],
        "THERMAL",
        {"RADIATOR_TEMP": 5200, "RW_TEMP": 6100, "BUS_TEMP": 4800, "HEATER_DUTY": 1465},
    )
    assert frame.limits_overall == "BLUE"
    assert frame.cosmos_overall_limits_state() == "GREEN"


def test_point_representation_modes(targets):
    frame = TelemetryFrame.from_dn(targets["DROID_SIM"], "EPS", {"BATT_VOLTAGE": 800})
    point = frame.point("BATT_VOLTAGE")
    assert point.value_for(ValueType.RAW) == 800
    assert point.value_for(ValueType.CONVERTED) == pytest.approx(15.6)
    assert point.represent(ValueType.FORMATTED) == "15.600 V"
