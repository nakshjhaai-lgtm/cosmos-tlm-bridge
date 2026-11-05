"""``cosmos-regress`` / ``cosmos-tlm-bridge`` command line.

    uvx cosmos-tlm-bridge demo                     # full self-contained acceptance run
    cosmos-regress run droid_pass_acceptance.yaml   # run against a live COSMOS
    cosmos-tlm-bridge serve                         # start the DROID_SIM reference server
    cosmos-tlm-bridge bridge --webhook http://localhost:8080/tlm
    cosmos-tlm-bridge verify DROID_SIM__EPS__BATT_VOLTAGE --dn-eu EU --limits
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sys
import textwrap
import time
from pathlib import Path
from typing import Any

from . import __version__
from .client import CosmosClient, CosmosError
from .models import LimitsColor, LimitsState, ValueType, load_target
from .telemetry import build_channel, channel_parts

ACCENT = "\033[36m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RED = "\033[31m"
DIM = "\033[2m"
BOLD = "\033[1m"
RESET = "\033[0m"


def _c(text: str, color: str) -> str:
    if not sys.stdout.isatty() or os.environ.get("NO_COLOR"):
        return text
    return f"{color}{text}{RESET}"


def _pkg_definitions_dir() -> Path:
    here = Path(__file__).resolve()
    for candidate in (
        here.parent / "_bundled" / "definitions" / "droid_sim",   # installed wheel
        here.parents[2] / "definitions" / "droid_sim",            # source checkout
        Path.cwd() / "definitions" / "droid_sim",
        Path("/app/definitions/droid_sim"),                       # container
    ):
        if candidate.is_dir():
            return candidate
    raise FileNotFoundError("could not locate definitions/droid_sim")


def _bundled_suite(name: str) -> Path | None:
    here = Path(__file__).resolve()
    for base in (
        here.parent / "_bundled" / "suites",   # installed wheel
        here.parents[2] / "suites",            # source checkout
        Path.cwd() / "suites",
        Path("/app/suites"),                   # container
    ):
        candidate = base / name
        if candidate.is_file():
            return candidate
    return None


# --------------------------------------------------------------------------- #
# helpers
# --------------------------------------------------------------------------- #


def _make_client(args: argparse.Namespace) -> CosmosClient:
    return CosmosClient(
        base_url=getattr(args, "cosmos_url", None) or "http://localhost:2900",
        scope=getattr(args, "scope", None) or "DEFAULT",
        password=getattr(args, "password", None),
        auth_mode=getattr(args, "auth_mode", "keyless"),
        timeout=getattr(args, "timeout", 15.0),
    )


def _load_target(args: argparse.Namespace) -> Any:
    ddir = getattr(args, "definitions", None) or _pkg_definitions_dir()
    return load_target(ddir, getattr(args, "target", "DROID_SIM"))


def _event_printer(quiet: bool = False):
    def on_event(event: str, payload: dict[str, Any]) -> None:
        if quiet:
            return
        if event == "case":
            status = payload["status"]
            color = {"passed": GREEN, "failed": RED, "error": RED}.get(status, DIM)
            mark = {"passed": "PASS", "failed": "FAIL", "error": "ERR "}.get(status, "SKIP")
            print(f"  {_c(mark, color)} {payload['name']}")
            if status != "passed" and payload.get("msg"):
                for line in textwrap.wrap(payload["msg"], 100)[:6]:
                    print(f"       {_c(line, DIM)}")
        elif event == "suite_start":
            print(f"\n{BOLD}suite{RESET} {payload['suite']} "
                  f"{DIM}(budget {payload['timeout_s']}s){RESET}")
        elif event == "suite_end":
            counts = payload["counts"]
            verdict = _c("PASS", GREEN) if payload["passed"] else _c("FAIL", RED)
            print(f"  -> {verdict} {counts.get('passed',0)} passed, "
                  f"{counts.get('failed',0)} failed, {counts.get('error',0)} errors")

    return on_event


def _print_banner(target: str, url: str, scope: str) -> None:
    print(f"{_c('cosmos-tlm-bridge', ACCENT)} {DIM}v{__version__}{RESET}")
    print(f"{DIM}OpenC3 COSMOS v5 telemetry-to-microservice bridge{RESET}")
    print(f"  target   {BOLD}{target}{RESET}")
    print(f"  cosmos   {url}  {DIM}(scope {scope}){RESET}")
    print(f"  keyless  {_c('no API key, no Ruby scripts', GREEN)}")


# --------------------------------------------------------------------------- #
# demo
# --------------------------------------------------------------------------- #


def cmd_demo(args: argparse.Namespace) -> int:
    from .mock.server import MockCosmosServer
    from .regress.report import write_all
    from .regress.runner import RegressionRunner
    from .regress.schema import load_suite

    t0 = time.monotonic()
    url = (
        getattr(args, "cosmos_url", None)
        or os.environ.get("COSMOS_URL")
        or f"http://127.0.0.1:{args.port}"
    )
    _print_banner(args.target, url, args.scope)
    args.cosmos_url = url
    print(f"\n{BOLD}1/4{RESET} booting DROID_SIM OpenC3 reference server ...", end="", flush=True)

    server = MockCosmosServer(host="0.0.0.0", port=args.port, scope=args.scope)
    server.start()
    if not server.wait_ready(timeout=20):
        print(_c(" FAILED", RED))
        return 2
    boot = time.monotonic() - t0
    print(f" {_c('ready', GREEN)} {DIM}({boot:.2f}s){RESET}")

    client = _make_client(args)
    target_def = _load_target(args)

    print(f"{BOLD}2/4{RESET} verifying JSON-RPC + typed decommutation ...", end="", flush=True)
    try:
        status = client.get_server_status()
        frame = client.get_tlm_packet(args.target, "EPS", "CONVERTED")
    except CosmosError as exc:
        print(_c(f" FAILED: {exc}", RED))
        server.stop()
        return 3
    print(f" {_c('ok', GREEN)} {DIM}({len(frame)} points on EPS, "
          f"server {status.get('version','?')}){RESET}")

    suite_path = Path(args.suite)
    if not suite_path.is_file():
        bundled = _bundled_suite(args.suite)
        if bundled is None:
            print(_c(f"FAILED: suite {args.suite!r} not found", RED))
            server.stop()
            return 4
        suite_path = bundled
    suite = load_suite(suite_path)

    print(f"{BOLD}3/4{RESET} running acceptance suite {_c(suite.suite, ACCENT)}")
    runner = RegressionRunner(
        client, target_def, verbose=args.verbose, on_event=_event_printer(args.quiet)
    )
    started = time.monotonic()
    result = runner.run(suite)
    suite_elapsed = time.monotonic() - started

    print(f"\n{BOLD}4/4{RESET} writing reports ...", end="", flush=True)
    outdir = Path(args.out)
    reports = write_all([result], outdir, stem="droid_pass")
    if args.verbose:
        reports["bridge"] = _write_bridge_artifacts(client, target_def, outdir, args)
    total = time.monotonic() - t0
    print(f" {_c('done', GREEN)}")

    for kind, path in reports.items():
        print(f"  {kind:<8} {path}")

    print(
        f"\n{_c('acceptance summary', BOLD)}  "
        f"{'PASS' if result.passed else 'FAIL'}  "
        f"{DIM}(suite {suite_elapsed:.1f}s / total {total:.1f}s, "
        f"budget {args.budget}s){RESET}"
    )
    for case in result.details if hasattr(result, "details") else []:  # pragma: no cover
        print(case)
    for case in result.cases:
        color = {"passed": GREEN, "failed": RED, "error": RED}.get(case.status, DIM)
        print(f"  {_c(case.status.upper().ljust(6), color)} {case.name}")
    if result.errors:
        for err in result.errors:
            print(_c(f"  error: {err}", RED))

    within = total < args.budget
    print(
        f"\n{_c('instant spin-up', BOLD)} {total:5.1f}s / {args.budget}s "
        f"{_c('PASS', GREEN) if within else _c('FAIL', RED)}"
    )
    server.stop()
    return 0 if (result.passed and within) else 1


def _write_bridge_artifacts(client, target_def, outdir: Path, args) -> Path:
    """Optional: dump the typed frames the bridge would forward downstream."""
    from .bridge import TelemetryBridge

    bridge = TelemetryBridge(client, target_def, poll_hz=2)
    frames = [bridge.poll_once(p) for p in ("ADCS", "EPS", "THERMAL")]
    for f in frames:
        bridge.emit(f)
    out = outdir / "bridge-frames.json"
    out.write_text(
        json.dumps([f.to_webhook_payload() for f in frames], indent=2),
        encoding="utf-8",
    )
    return out


# --------------------------------------------------------------------------- #
# run
# --------------------------------------------------------------------------- #


def cmd_run(args: argparse.Namespace) -> int:
    from .regress.report import write_all
    from .regress.runner import RegressionRunner
    from .regress.schema import load_suite

    path = Path(args.suite)
    if not path.is_file():
        bundled = _bundled_suite(args.suite)
        if bundled is None:
            print(_c(f"suite not found: {args.suite}", RED), file=sys.stderr)
            return 4
        path = bundled
    suite = load_suite(path)

    client = _make_client(args)
    try:
        if not client.health():
            print(
                _c(
                    f"cannot reach COSMOS at {client.base_url}. "
                    "Start one with `cosmos-tlm-bridge serve` or `docker compose up`.",
                    RED,
                ),
                file=sys.stderr,
            )
            return 3
    except CosmosError as exc:
        print(_c(str(exc), RED), file=sys.stderr)
        return 3

    target_def = _load_target(args)
    runner = RegressionRunner(
        client, target_def, verbose=args.verbose, on_event=_event_printer(args.quiet)
    )
    result = runner.run(suite)

    outdir = Path(args.out)
    stem = Path(path).stem
    reports = write_all([result], outdir, stem=stem)
    for kind, p in reports.items():
        print(f"{kind:<6} {p}")

    counts = result.counts
    print(
        f"\n{'PASS' if result.passed else 'FAIL'}: "
        f"{counts.get('passed',0)} passed, {counts.get('failed',0)} failed, "
        f"{counts.get('error',0)} errors, {result.duration_s:.1f}s"
    )
    return 0 if result.passed else 1


# --------------------------------------------------------------------------- #
# serve / bridge / verify / limits / targets
# --------------------------------------------------------------------------- #


def cmd_serve(args: argparse.Namespace) -> int:
    from .mock.server import serve

    print(f"{_c('cosmos-tlm-bridge', ACCENT)} {DIM}v{__version__}{RESET}")
    print("starting DROID_SIM OpenC3 COSMOS v5 reference server "
          f"on {args.host}:{args.port} (scope {args.scope})\n")
    serve(
        host=args.host,
        port=args.port,
        scope=args.scope,
        seed=args.seed,
        stream_rate_hz=args.stream_rate,
        auth_required=args.auth_required,
        password=args.password or "cosmos",
    )
    return 0


def cmd_bridge(args: argparse.Namespace) -> int:
    from .bridge import TelemetryBridge, WebhookSink

    client = _make_client(args)
    target_def = _load_target(args)
    sinks = [WebhookSink(u, dry_run=args.dry_run) for u in (args.webhook or [])]
    bridge = TelemetryBridge(
        client, target_def, webhooks=sinks, poll_hz=args.poll_hz, scope=args.scope
    )

    def show(frame):
        summary = frame.limits_summary()
        color = {"GREEN": GREEN, "BLUE": ACCENT, "YELLOW": YELLOW, "RED": RED}[
            summary.overall.value
        ]
        if args.json:
            print(json.dumps(frame.to_webhook_payload()))
            return
        parts = " ".join(
            f"{n}={p.eu:g}" for n, p in list(sorted(frame.points.items()))[:4]
        )
        print(
            f"{frame.packet:<8} {_c(summary.overall.value.ljust(6), color)} "
            f"{parts}"
        )

    bridge.on_frame(show)
    bridge.on_violation(
        lambda f, v: print(_c(f"  !! limits violation on {f.packet}: {v['items']}", YELLOW))
    )
    print(
        f"{_c('bridging', ACCENT)} {args.target} "
        f"{DIM}packets={','.join(args.packet)} webhooks={len(sinks)} "
        f"for {args.duration}s{RESET}"
    )
    stats = bridge.run(args.packet, duration=args.duration, max_frames=args.max_frames)
    print(
        f"\nframes={stats.frames} points={stats.points} "
        f"webhook_ok={stats.webhook_ok} webhook_failed={stats.webhook_failed} "
        f"violations={stats.violations}"
    )
    if stats.errors:
        print(_c(f"warnings: {stats.errors[:3]}", YELLOW))
    return 0 if stats.frames > 0 and stats.webhook_failed == 0 else 1


def cmd_verify(args: argparse.Namespace) -> int:
    client = _make_client(args)
    target_def = _load_target(args)
    channels = args.channel
    rows: list[dict[str, Any]] = []
    exit_code = 0
    for channel in channels:
        try:
            target, packet, point = channel_parts(channel, args.target)
        except ValueError as exc:
            print(_c(str(exc), RED), file=sys.stderr)
            return 2
        item = target_def.item(packet, point)
        try:
            dn = client.tlm(target, packet, point, "RAW")
            eu = client.tlm(target, packet, point, "CONVERTED")
            fmt = client.tlm_with_units(target, packet, point)
            state = client.rpc(
                "get_tlm_values", [[[target, packet, point]]], type="CONVERTED"
            )[1][0]
        except CosmosError as exc:
            print(_c(f"{build_channel(target,packet,point)}: {exc}", RED), file=sys.stderr)
            exit_code = 3
            continue
        limits_state = LimitsState.from_token(str(state))
        row = {
            "channel": build_channel(target, packet, point),
            "dn": dn,
            "eu": eu,
            "formatted": fmt,
            "units": item.units,
            "limits_state": limits_state.value,
            "limits_color": limits_state.color.value,
            "limits": item.limits.model_dump() if item.limits else None,
        }
        rows.append(row)
        color = {
            LimitsColor.GREEN: GREEN,
            LimitsColor.BLUE: ACCENT,
            LimitsColor.YELLOW: YELLOW,
            LimitsColor.RED: RED,
        }[limits_state.color]
        print(
            f"{row['channel']:<34} DN={dn:<8} EU={eu:<10.4f} {fmt:<16} "
            f"{_c(limits_state.value, color)}"
        )
        if args.limits and item.limits:
            lim = item.limits
            dn_lim = item.limits_in_dn()
            print(
                f"  {DIM}EU limits  red_low={lim.red_low} yellow_low={lim.yellow_low} "
                f"yellow_high={lim.yellow_high} red_high={lim.red_high} "
                f"blue=[{lim.green_low},{lim.green_high}]{RESET}"
            )
            if dn_lim is not None:
                print(
                    f"  {DIM}DN limits  red_low={dn_lim.red_low} yellow_low={dn_lim.yellow_low} "
                    f"yellow_high={dn_lim.yellow_high} red_high={dn_lim.red_high}{RESET}"
                )
    if args.json:
        print(json.dumps(rows, indent=2))
    return exit_code


def cmd_limits(args: argparse.Namespace) -> int:
    target_def = _load_target(args)
    for packet in sorted(target_def.packets):
        pkt = target_def.packet(packet)
        print(f"\n{BOLD}{target_def.name} {pkt.name}{RESET} {DIM}{pkt.description}{RESET}")
        print(
            f"  {'point':<16}{'units':<8}{'DN range':<18}"
            f"{'EU range':<22}{'blue (op)':<20}limits_state fn"
        )
        for name, item in pkt.items.items():
            if item.limits is None:
                continue
            lim = item.limits
            dn_lim = item.limits_in_dn() or lim
            role = "identity" if item.conversion.is_identity else (
                f"POLY {item.conversion.c0:g}+{item.conversion.c1:g}x"
            )
            print(
                f"  {name:<16}{item.units or '-':<8}"
                f"{f'{dn_lim.red_low:g}..{dn_lim.red_high:g}':<18}"
                f"{f'{lim.red_low:g}..{lim.red_high:g}':<22}"
                f"{f'[{lim.green_low:g},{lim.green_high:g}]' if lim.has_operational_band else '-':<20}"
                f"{role}"
            )
    return 0


def cmd_targets(args: argparse.Namespace) -> int:
    target_def = _load_target(args)
    print(f"{target_def.name}: {len(target_def.packets)} packets, "
          f"{len(target_def.all_channels())} typed points, "
          f"{len(target_def.commands)} commands")
    for packet in sorted(target_def.packets):
        pkt = target_def.packet(packet)
        print(f"\n  {BOLD}{packet}{RESET} — {len(pkt.items)} points")
        for name in pkt.items:
            print(f"    {build_channel(target_def.name, packet, name)}")
    if target_def.commands:
        print(f"\n  commands: {', '.join(sorted(target_def.commands))}")
    return 0


def cmd_version(args: argparse.Namespace) -> int:
    print(f"cosmos-tlm-bridge {__version__}")
    return 0


# --------------------------------------------------------------------------- #
# parser
# --------------------------------------------------------------------------- #


def _common(p: argparse.ArgumentParser, *, url: bool = True) -> None:
    if url:
        p.add_argument("--cosmos-url", default=os.environ.get("COSMOS_URL", "http://localhost:2900"),
                       help="COSMOS base URL (env COSMOS_URL)")
    p.add_argument("--scope", default=os.environ.get("COSMOS_SCOPE", "DEFAULT"))
    p.add_argument("--target", default="DROID_SIM")
    p.add_argument("--definitions", default=None, help="OpenC3 cmd_tlm definitions directory")
    p.add_argument("--auth-mode", default=os.environ.get("COSMOS_AUTH_MODE", "keyless"),
                   choices=["keyless", "core", "enterprise"])
    p.add_argument("--password", default=os.environ.get("COSMOS_PASSWORD"))
    p.add_argument("--timeout", type=float, default=15.0)
    p.add_argument("-v", "--verbose", action="store_true")
    p.add_argument("-q", "--quiet", action="store_true")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="cosmos-tlm-bridge",
        description="OpenC3 COSMOS v5 telemetry-to-microservice bridge + CI regression runner",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=textwrap.dedent(
            """
            examples:
              uvx cosmos-tlm-bridge demo
              cosmos-regress run droid_pass_acceptance.yaml
              cosmos-tlm-bridge serve --port 2900
              cosmos-tlm-bridge bridge --packet EPS --webhook http://localhost:8080/tlm
              cosmos-tlm-bridge verify DROID_SIM__THERMAL__RW_TEMP --limits
              cosmos-tlm-bridge limits
            """
        ),
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    demo = sub.add_parser("demo", help="self-contained DROID pass acceptance run")
    _common(demo, url=False)
    demo.add_argument("--cosmos-url", default=None, help=argparse.SUPPRESS)
    demo.add_argument("--port", type=int, default=2900)
    demo.add_argument("--suite", default="droid_pass_acceptance.yaml")
    demo.add_argument("--out", default="artifacts")
    demo.add_argument("--budget", type=float, default=45.0,
                      help="spin-up budget in seconds (default 45)")
    demo.set_defaults(func=cmd_demo)

    run = sub.add_parser("run", help="run a declarative YAML acceptance suite")
    run.add_argument("suite")
    _common(run)
    run.add_argument("--out", default="artifacts")
    run.set_defaults(func=cmd_run)

    serve = sub.add_parser("serve", help="start the DROID_SIM OpenC3 reference server")
    serve.add_argument("--host", default="0.0.0.0")
    serve.add_argument("--port", type=int, default=2900)
    serve.add_argument("--scope", default="DEFAULT")
    serve.add_argument("--seed", type=int, default=1337)
    serve.add_argument("--stream-rate", type=float, default=4.0)
    serve.add_argument("--auth-required", action="store_true")
    serve.add_argument("--password", default="cosmos")
    serve.set_defaults(func=cmd_serve)

    bridge = sub.add_parser("bridge", help="stream typed frames to downstream microservices")
    _common(bridge)
    bridge.add_argument("--packet", nargs="+", default=["ADCS", "EPS", "THERMAL"])
    bridge.add_argument("--webhook", action="append", default=[])
    bridge.add_argument("--duration", type=float, default=10.0)
    bridge.add_argument("--max-frames", type=int, default=None)
    bridge.add_argument("--poll-hz", type=float, default=4.0)
    bridge.add_argument("--json", action="store_true")
    bridge.add_argument("--dry-run", action="store_true")
    bridge.set_defaults(func=cmd_bridge)

    verify = sub.add_parser("verify", help="read channels in DN and EU and show limits state")
    _common(verify)
    verify.add_argument("channel", nargs="+")
    verify.add_argument("--dn-eu", default="BOTH", choices=["DN", "EU", "BOTH"])
    verify.add_argument("--limits", action="store_true")
    verify.add_argument("--json", action="store_true")
    verify.set_defaults(func=cmd_verify)

    limits = sub.add_parser("limits", help="print the typed limits table (EU and DN domains)")
    _common(limits)
    limits.set_defaults(func=cmd_limits)

    targets = sub.add_parser("targets", help="list typed channels for the target")
    _common(targets)
    targets.set_defaults(func=cmd_targets)

    version = sub.add_parser("version", help="print version")
    version.set_defaults(func=cmd_version)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.func(args) or 0)
    except KeyboardInterrupt:
        print("\ninterrupted", file=sys.stderr)
        return 130
    except FileNotFoundError as exc:
        print(_c(f"error: {exc}", RED), file=sys.stderr)
        return 2
    except CosmosError as exc:
        print(_c(f"cosmos error: {exc}", RED), file=sys.stderr)
        return 3


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
