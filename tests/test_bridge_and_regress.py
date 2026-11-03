"""Integration tests: the DROID_SIM server, the typed bridge, and the
declarative regression runner — including a negative control proving the
assertions catch a missing YELLOW_HIGH violation.
"""

from __future__ import annotations

import socket
from pathlib import Path

import pytest

from cosmos_tlm_bridge.bridge import PointSubscription, TelemetryBridge, WebhookSink
from cosmos_tlm_bridge.client import CosmosClient, CosmosRPCError
from cosmos_tlm_bridge.models import LimitsState, ValueType, load_target
from cosmos_tlm_bridge.mock.server import MockCosmosServer
from cosmos_tlm_bridge.regress.report import write_html, write_junit
from cosmos_tlm_bridge.regress.runner import RegressionRunner
from cosmos_tlm_bridge.regress.schema import (
    AssertionSpec,
    Condition,
    SuiteSpec,
    load_suite,
    load_suite_text,
)

ROOT = Path(__file__).resolve().parents[1]
SUITES = ROOT / "suites"


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture(scope="module")
def cosmos():
    server = MockCosmosServer(host="127.0.0.1", port=free_port())
    server.start()
    assert server.wait_ready(10)
    yield server
    server.stop()


@pytest.fixture(scope="module")
def client(cosmos):
    return CosmosClient(base_url=cosmos.url, auth_mode="keyless")


@pytest.fixture(scope="module")
def target_def():
    return load_target(ROOT / "definitions" / "droid_sim", "DROID_SIM")


# --------------------------------------------------------------------------- #
# mock server / JSON-RPC surface
# --------------------------------------------------------------------------- #


def test_server_status(client):
    status = client.get_server_status()
    assert status["targets"] == ["DROID_SIM"]
    assert status["auth_required"] is False


def test_tlm_both_param_forms_agree(client):
    single = client.rpc("tlm", ["DROID_SIM EPS BATT_VOLTAGE"])
    triple = client.rpc("tlm", ["DROID_SIM", "EPS", "BATT_VOLTAGE"])
    assert single == triple


def test_raw_and_converted_differ(client):
    raw = client.tlm("DROID_SIM", "EPS", "BATT_VOLTAGE", "RAW")
    eu = client.tlm("DROID_SIM", "EPS", "BATT_VOLTAGE", "CONVERTED")
    assert isinstance(raw, int)
    assert eu == pytest.approx(13.6 + 0.0025 * raw)


def test_get_tlm_packet_returns_triples(client):
    rows = client.get_tlm_packet("DROID_SIM", "THERMAL", "CONVERTED")
    assert len(rows) == 9
    assert all(len(r) == 3 for r in rows)
    names = {r[0] for r in rows}
    assert {"RADIATOR_TEMP", "RW_TEMP", "BUS_TEMP", "HEATER_DUTY"} <= names


def test_get_tlm_values_batch(client):
    values, states, settings, limits_set = client.get_tlm_values(
        [["DROID_SIM", "EPS", "BATT_VOLTAGE"], ["DROID_SIM", "THERMAL", "RW_TEMP"]]
    )
    assert len(values) == len(states) == 2
    assert limits_set == "DEFAULT"
    assert states[1] in {s.value for s in LimitsState}


def test_unknown_method_returns_rpc_error(client):
    with pytest.raises(CosmosRPCError):
        client.rpc("no_such_method", [])


def test_get_tlm_list(client):
    listing = client.get_tlm_list("DROID_SIM")
    assert {p for p, _ in listing} == {"ADCS", "EPS", "THERMAL"}


def test_core_auth_mode_roundtrip():
    port = free_port()
    server = MockCosmosServer(host="127.0.0.1", port=port, auth_required=True, password="s3cret")
    server.start()
    try:
        assert server.wait_ready(10)
        ok = CosmosClient(base_url=server.url, auth_mode="core", password="s3cret")
        assert ok.login() is not None
        assert ok.get_server_status()["targets"] == ["DROID_SIM"]

        bad = CosmosClient(base_url=server.url, auth_mode="core", password="wrong")
        with pytest.raises(Exception):
            bad.login()
    finally:
        server.stop()


# --------------------------------------------------------------------------- #
# typed bridge
# --------------------------------------------------------------------------- #


def test_bridge_poll_produces_typed_frame(client, target_def):
    bridge = TelemetryBridge(client, target_def)
    frame = bridge.poll_once("EPS")
    assert frame.target == "DROID_SIM"
    assert frame.packet == "EPS"
    assert frame.points["BATT_VOLTAGE"].channel == "DROID_SIM__EPS__BATT_VOLTAGE"
    assert isinstance(frame.points["BATT_VOLTAGE"].eu, float)
    # bridge EU must match COSMOS' conversion
    assert frame.points["BATT_VOLTAGE"].eu == pytest.approx(
        client.tlm("DROID_SIM", "EPS", "BATT_VOLTAGE", "CONVERTED")
    )


def test_bridge_has_no_limits_divergence(client, target_def):
    bridge = TelemetryBridge(client, target_def)
    for packet in ("ADCS", "EPS", "THERMAL"):
        bridge.poll_once(packet)
    assert bridge.stats.errors == []


def test_bridge_emit_forwards_to_webhook(client, target_def):
    sent: list[dict] = []

    class Recorder:
        def send(self, payload):
            sent.append(payload)
            return True

    bridge = TelemetryBridge(client, target_def, webhooks=[Recorder()])  # type: ignore[list-item]
    frame = bridge.poll_once("EPS")
    bridge.emit(frame)
    assert bridge.stats.frames == 1
    assert bridge.stats.webhook_ok == 1
    assert sent[0]["topic"] == "DEFAULT__DECOM__{DROID_SIM}__EPS"
    assert "cosmos_overall_limits_state" in sent[0]


def test_webhook_failure_is_counted_not_raised(client, target_def):
    sink = WebhookSink("http://127.0.0.1:1/nope", retries=1, timeout=0.2)
    bridge = TelemetryBridge(client, target_def, webhooks=[sink])
    bridge.emit(bridge.poll_once("EPS"))
    assert bridge.stats.webhook_failed == 1


def test_subscription_keys_match_cosmos_streaming_format(client, target_def):
    bridge = TelemetryBridge(client, target_def)
    subs = bridge.resolve_channels(["THERMAL__RW_TEMP:DN", "EPS__BATT_VOLTAGE:EU"])
    assert subs[0].value_type is ValueType.RAW
    assert subs[0].streaming_key == "DECOM__TLM__DROID_SIM__THERMAL__RW_TEMP__RAW__"
    assert subs[1].streaming_key == "DECOM__TLM__DROID_SIM__EPS__BATT_VOLTAGE__CONVERTED__"


def test_resolve_channels_rejects_unknown_point(client, target_def):
    bridge = TelemetryBridge(client, target_def)
    with pytest.raises(KeyError):
        bridge.resolve_channels(["THERMAL__NOT_A_POINT"])


def test_client_stream_topics(client):
    assert client.stream_topic("DROID_SIM", "EPS") == "DEFAULT__DECOM__{DROID_SIM}__EPS"


def test_ndjson_stream_delivers_frames(client):
    frames = list(client.subscribe("DROID_SIM", "EPS", max_frames=3, timeout=10))
    assert len(frames) == 3
    assert frames[0]["frame"]["packet"] == "EPS"
    assert "points" in frames[0]["frame"]


# --------------------------------------------------------------------------- #
# regression schema
# --------------------------------------------------------------------------- #


@pytest.mark.parametrize(
    ("raw", "comparison", "operands"),
    [
        ("EQUAL,4,,,", "EQUAL", (4.0,)),
        ("BETWEEN,1.0,2.0,,", "BETWEEN", (1.0, 2.0)),
        (">= 0.5", "GTE", (0.5,)),
        ("< 55.0", "LT", (55.0,)),
        ("1..3", "BETWEEN", (1.0, 3.0)),
    ],
)
def test_condition_parsing(raw, comparison, operands):
    cond = Condition.parse(raw)
    assert cond.comparison.value == comparison
    assert cond.operands == operands


def test_condition_evaluation():
    assert Condition.parse("BETWEEN,1.0,2.0,,").check(1.5)[0] is True
    assert Condition.parse("BETWEEN,1.0,2.0,,").check(2.5)[0] is False
    assert Condition.parse(">= 70.0").check(70.0)[0] is True
    assert Condition.parse("CHANGE,0.5,,").check(1.2, prior=0.5)[0] is True
    assert Condition.parse("CHANGE,0.5,,").check(0.6, prior=0.5)[0] is False
    assert Condition.parse("EQUAL,4,,,").check(4.2, prior=None)[0] is False


def test_assertion_requires_a_predicate():
    with pytest.raises(ValueError):
        AssertionSpec(channel="EPS__BATT_VOLTAGE")


def test_assertion_dn_eu_validation():
    with pytest.raises(ValueError):
        AssertionSpec(channel="EPS__BATT_VOLTAGE", dn_eu="PARSECS", condition="> 1")
    spec = AssertionSpec(channel="EPS__BATT_VOLTAGE", dn_eu="dn", condition="> 1")
    assert spec.dn_eu == "DN"
    assert spec.value_type is ValueType.RAW


def test_limits_token_aliases():
    assert LimitsState.from_token("yellow high") is LimitsState.YELLOW_HIGH
    assert LimitsState.from_token("YELLOW") is LimitsState.YELLOW_HIGH
    assert LimitsState.from_token("RED") is LimitsState.RED_HIGH
    assert LimitsState.from_token("operational") is LimitsState.BLUE


def test_suite_files_load():
    for name in ("droid_pass_acceptance.yaml", "droid_smoke.yaml", "droid_thermal_red.yaml"):
        suite = load_suite(SUITES / name)
        assert suite.target == "DROID_SIM"
        assert suite.steps
        assert suite.timeout_s <= 105


def test_suite_rejects_unknown_step():
    with pytest.raises(ValueError):
        SuiteSpec(suite="x", steps=[{"teleport": True}]).normalised_steps()


def test_acceptance_suite_covers_dn_and_eu():
    suite = load_suite(SUITES / "droid_pass_acceptance.yaml")
    modes = set()
    for kind, step in suite.normalised_steps():
        if kind == "assert":
            modes |= {a.dn_eu for a in step.assert_}
    assert {"DN", "EU"} <= modes


# --------------------------------------------------------------------------- #
# runner: positive and negative control
# --------------------------------------------------------------------------- #


@pytest.fixture(scope="module")
def runner(client, target_def):
    return RegressionRunner(client, target_def)


def test_smoke_suite_passes(runner):
    result = runner.run(load_suite(SUITES / "droid_smoke.yaml"))
    assert result.passed, [c.message for c in result.cases if c.failed]
    assert result.total >= 3


def test_acceptance_suite_catches_yellow_high(runner):
    result = runner.run(load_suite(SUITES / "droid_pass_acceptance.yaml"))
    assert result.passed, [c.message for c in result.cases if c.failed]
    names = " ".join(c.name for c in result.cases)
    assert "EU mode" in names and "DN mode" in names
    assert result.duration_s < 105


def test_negative_control_wrong_limits_state_fails(runner):
    """A suite that expects RED where only YELLOW occurs must FAIL."""
    suite = SuiteSpec(
        suite="negative_control",
        fresh_sim=True,
        timeout_s=45,
        steps=[
            {"cmd": "DROID_SIM CMD_SLEW_TARGET with SLEW_RATE 2.5, TARGET_ID 7, DURATION 60"},
            {
                "assert": [
                    {
                        "channel": "THERMAL__RW_TEMP",
                        "dn_eu": "EU",
                        "expect_limits": "RED_HIGH",
                        "within_s": 20,
                    }
                ]
            },
        ],
    )
    result = runner.run(suite)
    assert result.passed is False
    failed = [c for c in result.cases if c.failed]
    assert failed
    assert "RED_HIGH" in failed[0].message


def test_negative_control_wrong_value_fails(runner):
    suite = SuiteSpec(
        suite="negative_value",
        fresh_sim=True,
        timeout_s=20,
        steps=[
            {
                "assert": [
                    {
                        "channel": "EPS__BATT_VOLTAGE",
                        "dn_eu": "EU",
                        "condition": "BETWEEN,99.0,100.0,,",
                        "within_s": 3,
                    }
                ]
            }
        ],
    )
    result = runner.run(suite)
    assert result.passed is False


def test_runner_reports_command_failure(runner):
    suite = SuiteSpec(
        suite="bad_cmd",
        timeout_s=10,
        steps=[{"cmd": "NOT_A_TARGET NOT_A_COMMAND"}],
    )
    result = runner.run(suite)
    assert result.passed is False


# --------------------------------------------------------------------------- #
# reports
# --------------------------------------------------------------------------- #


def test_junit_and_html_reports(runner, tmp_path):
    result = runner.run(load_suite(SUITES / "droid_smoke.yaml"))
    junit = write_junit([result], tmp_path / "junit.xml")
    html = write_html([result], tmp_path / "report.html")

    xml = junit.read_text()
    assert "<testsuites" in xml
    assert f'tests="{result.total}"' in xml
    assert "failure" not in xml or result.passed

    page = html.read_text()
    assert page.startswith("<!doctype html>")
    assert "PASS" in page
    assert "http://" not in page.split("<body>")[1] or "cdn" not in page
    assert "droid_smoke" in page


def test_junit_records_failures(runner, tmp_path):
    suite = SuiteSpec(
        suite="will_fail",
        timeout_s=15,
        steps=[
            {
                "assert": [
                    {"channel": "EPS__BATT_VOLTAGE", "dn_eu": "EU", "condition": "> 999", "within_s": 2}
                ]
            }
        ],
    )
    result = runner.run(suite)
    assert not result.passed
    xml = write_junit([result], tmp_path / "fail.xml").read_text()
    assert "<failure" in xml
    assert 'failures="1"' in xml
