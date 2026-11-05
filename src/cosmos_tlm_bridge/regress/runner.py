"""The CI regression runner.

Executes a :class:`SuiteSpec` against a live COSMOS instance (or the bundled
``DROID_SIM`` server) and produces a machine-readable result tree that the
JUnit and HTML reporters render.
"""

from __future__ import annotations

import time
import traceback
from dataclasses import dataclass, field
from typing import Any, Callable

from ..client import CosmosClient, CosmosError
from ..models import LimitsState, TargetDefinition, ValueType
from ..telemetry import build_channel, channel_parts
from .schema import (
    AssertStep,
    AssertionSpec,
    CommandStep,
    SuiteSpec,
    WaitForLimitsStep,
    WaitForPacketStep,
    WaitStep,
)

__all__ = ["CaseResult", "SuiteResult", "RegressionRunner"]


@dataclass
class CaseResult:
    name: str
    kind: str
    status: str = "passed"  # passed | failed | skipped | error
    duration_s: float = 0.0
    message: str = ""
    details: list[dict[str, Any]] = field(default_factory=list)
    samples: int = 0
    stdout: str = ""

    @property
    def failed(self) -> bool:
        return self.status in {"failed", "error"}


@dataclass
class SuiteResult:
    suite: str
    target: str
    scope: str
    started_at: float
    duration_s: float = 0.0
    cases: list[CaseResult] = field(default_factory=list)
    environment: dict[str, Any] = field(default_factory=dict)
    errors: list[str] = field(default_factory=list)

    @property
    def passed(self) -> bool:
        return not self.errors and all(not c.failed for c in self.cases)

    @property
    def counts(self) -> dict[str, int]:
        out = {"passed": 0, "failed": 0, "error": 0, "skipped": 0}
        for c in self.cases:
            out[c.status] = out.get(c.status, 0) + 1
        return out

    @property
    def total(self) -> int:
        return len(self.cases)


class RegressionRunner:
    """Drives a suite step by step, sampling telemetry for each assertion."""

    def __init__(
        self,
        client: CosmosClient,
        target_def: TargetDefinition,
        *,
        verbose: bool = False,
        on_event: Callable[[str, dict[str, Any]], None] | None = None,
    ) -> None:
        self.client = client
        self.target_def = target_def
        self.verbose = verbose
        self.on_event = on_event or (lambda _e, _p: None)
        self._prior: dict[str, float] = {}

    # -- telemetry sampling ------------------------------------------------ #

    def _channel(self, channel: str) -> tuple[str, str, str]:
        return channel_parts(channel, self.target_def.name)

    def sample(
        self,
        channel: str,
        value_type: ValueType,
        packet_cache: dict[str, Any],
    ) -> tuple[float, LimitsState]:
        """Read one point in the requested representation.

        DN reads hit the wire (``tlm_raw``), EU reads are computed by the same
        POLY conversion COSMOS applies, and the limits state always comes from
        COSMOS' own evaluation so a divergence would fail the run.
        """
        target, packet, point = self._channel(channel)
        cache_key = f"{packet}:{value_type.value}"
        if cache_key not in packet_cache:
            rows = self.client.get_tlm_packet(target, packet, value_type.value)
            packet_cache[cache_key] = {
                str(name).upper(): (value, str(state).upper()) for name, value, state in rows
            }
        row = packet_cache[cache_key].get(point)
        if row is None:
            raise KeyError(f"packet {target} {packet} has no point {point}")
        value, state = row
        try:
            limits_state = LimitsState.from_token(state)
        except ValueError:
            limits_state = LimitsState.UNKNOWN
        return float(value), limits_state

    # -- assertion evaluation --------------------------------------------- #

    def _evaluate(
        self, spec: AssertionSpec, packet_cache: dict[str, Any]
    ) -> tuple[bool, str, dict[str, Any]]:
        value, state = self.sample(spec.channel, spec.value_type, packet_cache)
        key = f"{spec.channel}:{spec.dn_eu}"
        prior = self._prior.get(key)
        self._prior[key] = value

        checks: list[tuple[bool, str]] = []
        if spec.condition is not None:
            ok, text = spec.condition.check(value, prior)
            checks.append((ok, f"value {text}"))
        if spec.expect_limits is not None:
            expected = spec.expect_limits
            ok = (
                state.color is expected.color
                if expected in {LimitsState.YELLOW_HIGH, LimitsState.RED_HIGH}
                and state in {LimitsState.YELLOW_LOW, LimitsState.RED_LOW}
                else state is expected
            )
            checks.append((ok, f"limits_state={state.value} expected={expected.value}"))
        if spec.expect_limits_color is not None:
            checks.append(
                (
                    state.color is spec.expect_limits_color,
                    f"color={state.color.value} expected={spec.expect_limits_color.value}",
                )
            )
        ok = all(c[0] for c in checks)
        detail = {
            "channel": build_channel(*self._channel(spec.channel)),
            "dn_eu": spec.dn_eu,
            "value": value,
            "limits_state": state.value,
            "limits_color": state.color.value,
            "checks": [{"ok": c[0], "detail": c[1]} for c in checks],
            "t": round(time.time(), 4),
        }
        return ok, "; ".join(c[1] for c in checks), detail

    def _run_assertion(self, spec: AssertionSpec, suite_deadline: float) -> CaseResult:
        start = time.monotonic()
        deadline = min(suite_deadline, start + spec.within_s)
        result = CaseResult(name=f"assert {spec.label}", kind="assert")
        observed: list[dict[str, Any]] = []
        satisfied = False
        last_message = "no samples collected"
        while time.monotonic() < deadline:
            cache: dict[str, Any] = {}
            try:
                ok, message, detail = self._evaluate(spec, cache)
            except (CosmosError, KeyError) as exc:
                result.status = "error"
                result.message = str(exc)
                result.duration_s = time.monotonic() - start
                return result
            result.samples += 1
            observed.append(detail)
            last_message = message
            if spec.occurrence.upper() == "ALWAYS":
                if not ok:
                    satisfied = False
                    last_message = f"{message} (ALWAYS violated)"
                    break
                satisfied = True
            elif ok:
                satisfied = True
                break
            time.sleep(spec.sample_interval_s)

        if spec.occurrence.upper() == "ALWAYS" and satisfied:
            last_message = "held for the whole window"
        result.status = "passed" if satisfied else "failed"
        result.message = (
            last_message if satisfied else f"timed out after {spec.within_s}s: {last_message}"
        )
        result.details = observed[-8:]
        result.duration_s = time.monotonic() - start
        self.on_event("assertion", {"spec": spec.label, "status": result.status, "detail": last_message})
        return result

    # -- steps ------------------------------------------------------------- #

    def _run_command(self, step: CommandStep) -> CaseResult:
        start = time.monotonic()
        name = step.name or f"cmd {step.cmd}"
        result = CaseResult(name=name, kind="command")
        try:
            response = self.client.cmd(step.cmd, step.check)
            result.details.append({"response": response})
            result.status = "passed"
            result.message = f"sent {step.cmd} (check={step.check})"
        except CosmosError as exc:
            result.status = "failed"
            result.message = str(exc)
        time.sleep(step.settle_s)
        result.duration_s = time.monotonic() - start
        self.on_event("command", {"cmd": step.cmd, "status": result.status})
        return result

    def _run_wait(self, step: WaitStep) -> CaseResult:
        start = time.monotonic()
        time.sleep(step.wait_s)
        return CaseResult(
            name=step.name or f"wait {step.wait_s}s",
            kind="wait",
            duration_s=time.monotonic() - start,
            message=step.reason or f"slept {step.wait_s}s",
        )

    def _run_wait_for_packet(self, step: WaitForPacketStep) -> CaseResult:
        start = time.monotonic()
        packet = step.wait_for_packet.upper()
        deadline = start + step.within_s
        result = CaseResult(name=step.name or f"wait for packet {packet}", kind="wait")
        while time.monotonic() < deadline:
            try:
                count = self.client.get_tlm_cnt(self.target_def.name, packet)
            except CosmosError as exc:
                result.status = "error"
                result.message = str(exc)
                return result
            if count and count > 0:
                result.status = "passed"
                result.message = f"{self.target_def.name} {packet} received ({count} packets)"
                result.duration_s = time.monotonic() - start
                return result
            time.sleep(0.1)
        result.status = "failed"
        result.message = f"no {packet} packets within {step.within_s}s"
        result.duration_s = time.monotonic() - start
        return result

    def _run_wait_for_limits(self, step: WaitForLimitsStep) -> CaseResult:
        start = time.monotonic()
        deadline = start + step.within_s
        result = CaseResult(
            name=step.name or f"wait for limits {step.wait_for_limits.value}", kind="wait"
        )
        packets = [step.packet.upper()] if step.packet else list(self.target_def.packets)
        while time.monotonic() < deadline:
            try:
                rows = self.client.get_out_of_limits()
            except CosmosError as exc:
                result.status = "error"
                result.message = str(exc)
                return result
            for row in rows:
                if len(row) < 4:
                    continue
                target, packet, item, state = row[0], row[1], row[2], row[3]
                if str(target).upper() != self.target_def.name:
                    continue
                if step.packet and str(packet).upper() not in packets:
                    continue
                try:
                    got = LimitsState.from_token(str(state))
                except ValueError:
                    continue
                if got is step.wait_for_limits or got.color is step.wait_for_limits.color:
                    result.status = "passed"
                    result.message = (
                        f"{packet} {item} entered {got.value} "
                        f"({round(time.monotonic() - start, 2)}s)"
                    )
                    result.details.append(
                        {"packet": packet, "item": item, "state": got.value}
                    )
                    result.duration_s = time.monotonic() - start
                    return result
            time.sleep(0.1)
        result.status = "failed"
        result.message = f"no {step.wait_for_limits.value} within {step.within_s}s"
        result.duration_s = time.monotonic() - start
        return result

    # -- suite ------------------------------------------------------------- #

    def run(self, suite: SuiteSpec) -> SuiteResult:
        started = time.time()
        result = SuiteResult(
            suite=suite.suite,
            target=suite.target,
            scope=suite.scope,
            started_at=started,
            environment={
                "cosmos_url": self.client.base_url,
                "scope": suite.scope,
                "target": suite.target,
                "auth_mode": self.client.auth_mode,
                "python": __import__("sys").version.split()[0],
            },
        )
        hard_deadline = time.monotonic() + suite.timeout_s
        self.on_event("suite_start", {"suite": suite.suite, "timeout_s": suite.timeout_s})

        if suite.fresh_sim:
            try:
                self.client.rpc("droid_sim_reset", [])
                self.on_event("reset", {"target": self.target_def.name})
            except CosmosError:
                # Not a DROID_SIM reference server; nothing to reset.
                pass

        try:
            for step in suite.setup:
                self._execute_dict(step, result, hard_deadline, phase="setup")
            if not result.passed:
                result.duration_s = time.time() - started
                return result

            for step in suite.steps:
                if time.monotonic() > hard_deadline:
                    result.errors.append(f"suite exceeded its {suite.timeout_s}s budget")
                    break
                self._execute_dict(step, result, hard_deadline, phase="steps")
            for step in suite.teardown:
                self._execute_dict(step, result, hard_deadline, phase="teardown")
        except Exception as exc:  # noqa: BLE001 - a runner crash is a suite error
            result.errors.append(f"{type(exc).__name__}: {exc}")
            result.errors.append(traceback.format_exc(limit=4))

        result.duration_s = time.time() - started
        self.on_event(
            "suite_end",
            {"suite": suite.suite, "passed": result.passed, "counts": result.counts},
        )
        return result

    def _execute_dict(
        self, raw: dict[str, Any], result: SuiteResult, deadline: float, phase: str
    ) -> None:
        from .schema import SuiteSpec

        try:
            normalised = SuiteSpec(
                suite="shim", steps=[raw], target=self.target_def.name
            ).normalised_steps()
        except ValueError as exc:
            result.cases.append(
                CaseResult(name=f"{phase}: {raw}", kind="error", status="error", message=str(exc))
            )
            return
        kind, step = normalised[0]
        label = phase

        if kind == "cmd":
            case = self._run_command(step)
        elif kind == "wait":
            case = self._run_wait(step)
        elif kind == "wait_for_packet":
            case = self._run_wait_for_packet(step)
        elif kind == "wait_for_limits":
            case = self._run_wait_for_limits(step)
        elif kind == "assert":
            case = self._run_assert_step(step, deadline)
        else:  # pragma: no cover
            case = CaseResult(name=f"{phase}: {raw}", kind=kind, status="error", message="unknown step")

        case.name = f"[{label}] {case.name}"
        if not self.verbose:
            case.stdout = ""
        result.cases.append(case)
        self.on_event("case", {"name": case.name, "status": case.status, "msg": case.message})

    def _run_assert_step(self, step: AssertStep, deadline: float) -> CaseResult:
        start = time.monotonic()
        name = step.name or f"assert ({len(step.assert_)} assertion(s))"
        results = [self._run_assertion(spec, deadline) for spec in step.assert_]
        merged = CaseResult(
            name=name,
            kind="assert",
            duration_s=time.monotonic() - start,
            samples=sum(r.samples for r in results),
        )
        failures = [r for r in results if r.failed]
        merged.details = [d for r in results for d in r.details]
        if failures:
            merged.status = "failed" if any(f.status == "failed" for f in failures) else "error"
            merged.message = " | ".join(f"{f.name}: {f.message}" for f in failures)
        else:
            merged.status = "passed"
            merged.message = "; ".join(f"{r.name}: {r.message}" for r in results)
        merged.stdout = "\n".join(f"{r.name} -> {r.status}: {r.message}" for r in results)
        return merged
