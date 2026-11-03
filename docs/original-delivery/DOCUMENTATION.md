# DOCUMENTATION: technical manual

Reference for both deliverables. Read `START-HERE.md` first for orientation and
`HANDOVER.md` for state/decisions. This file is the "how it works and how to use
it" manual.

Contents

1. [Project 1. cosmos-tlm-bridge](#1-project-1--cosmos-tlm-bridge)
 1. [Purpose and problem](#11-purpose-and-problem)
 2. [Architecture](#12-architecture)
 3. [The COSMOS protocol surface](#13-the-cosmos-protocol-surface)
 4. [Telemetry data model (DN/EU, limits, BLUE)](#14-telemetry-data-model)
 5. [Target definition (DROID_SIM)](#15-target-definition-droid_sim)
 6. [The bridge](#16-the-bridge)
 7. [Acceptance suites: YAML reference](#17-acceptance-suites-yaml-reference)
 8. [Regression runner semantics](#18-regression-runner-semantics)
 9. [Reports and artifacts](#19-reports-and-artifacts)
 10. [CLI reference](#110-cli-reference)
 11. [Go implementation](#111-go-implementation)
 12. [Docker / compose](#112-docker--compose)
 13. [Tests](#113-tests)
 14. [Configuration and limits of the system](#114-configuration-and-limits)
2. [Project 2. turion-platform-drift](#2-project-2--turion-platform-drift)
 1. [Purpose and problem](#21-purpose-and-problem)
 2. [Architecture](#22-architecture)
 3. [Scan pipeline](#23-scan-pipeline)
 4. [Rule catalog (26 rules)](#24-rule-catalog-26-rules)
 5. [Scoring model](#25-scoring-model)
 6. [Generator](#26-generator)
 7. [Validator](#27-validator)
 8. [Reports: text, JSON, SARIF, HTML](#28-reports)
 9. [CLI reference](#29-cli-reference)
 10. [Verification harness](#210-verification-harness)
3. [Cross-cutting conventions](#3-cross-cutting-conventions)

---

# 1. Project 1: cosmos-tlm-bridge

Folder: `PROJECT-1-cosmos-tlm-bridge/`.

## 1.1 Purpose and problem

Turion's ground segment tests each satellite software build by hand-maintaining
OpenC3 COSMOS Script Runner procedures. Ruby scripts that drive commands,
poll telemetry and assert limits. They are copied per mission, evolve silently,
and cannot be code-reviewed meaningfully. Downstream microservices also have to
speak COSMOS's JSON-RPC rather than receiving plain typed telemetry.

This package solves both halves:

1. A typed bridge that decommutates COSMOS packets into typed frames
 (value, units, limits state, DN and EU views) and pushes them to any HTTP
 webhook. so a microservice sees normal JSON, not `get_tlm_packet` tuples.
2. A declarative regression runner that expresses an acceptance procedure as
 YAML and emits CI-grade reports (JUnit XML, HTML, JSON) with a non-zero exit
 code on failure. No Ruby anywhere.

Both work against a keyless in-process mock COSMOS so CI needs no
credentials and no container runtime.

Contractual numbers: the full acceptance run must complete in < 45 s.

## 1.2 Architecture

```
                    ┌──────────────────────────────┐
   definitions/     │  src/cosmos_tlm_bridge/      │
   droid_sim/  ───► │  models.py    typed contract │
   cmd_tlm/*.txt    │  telemetry.py decommutation  │
                    │  client.py    JSON-RPC client│
                    │  bridge.py    fan-out engine │
                    │  mock/        keyless server │
                    │  regress/     suite runner   │
                    │  cli.py       argparse front │
                    └───────────┬──────────────────┘
                                │ HTTP JSON-RPC + StreamingApi
                    ┌───────────▼──────────┐
                    │ COSMOS v5 (/__openc3_api__/api)
                    │  mock (default) or real
                    └───────────┬──────────┘
                                │ typed frames
                    ┌───────────▼──────────┐
                    │ downstream webhook   │
                    │ (microservice / sink)│
                    └──────────────────────┘
```

Layer rules that must be preserved:

- `models.py` has no I/O and is the single source of truth for names,
 conversions, limits and severity. Everything else imports from it.
- `client.py` is the only module that speaks HTTP to COSMOS. The runner, the
 bridge and the CLI all go through it.
- `mock/` implements the *same* interface as a real COSMOS; it is a test double,
 not a shortcut. a real deployment swaps the URL, nothing else.
- `regress/` never contains target-specific knowledge; it consumes the suite
 file and the target definition.

Data flow for one pass:

```
mock plant (droid.py) → packets (ADCS/EPS/THERMAL) → COSMOS JSON-RPC
      → client.get_tlm_packet / get_limits → models.convert + limits evaluation
      → runner assertions (DN or EU) → CaseResult → JUnit/HTML/JSON
      ↳ or bridge: Frame → webhook POST
```

## 1.3 The COSMOS protocol surface

The mock and the client implement the subset OpenC3 documents for v5:

- Endpoint: `POST /__openc3_api__/api` with
 `{"jsonrpc":"2.0","id":1,"method":"<method>","params":[…],"keyword_params":{"scope":"DEFAULT"}}`.
 Default port 2900.
- Methods used: `get_target_list`, `get_packet_list`, `get_tlm_packet`,
 `get_tlm_values`, `get_limits`, `get_limits_events`, `cmd`,
 `get_cmd_list`, `get_out_of_limits`, `tlm`.
- Value domains: `tlm` (converted, EU), `tlm_raw` (raw counts, DN),
 `tlm_formatted`, `tlm_with_units`.
- `get_tlm_packet` returns `[[item, value, limits_state], …]` for one packet.
- `get_limits` returns `[set, persistence, enabled, red_low, yellow_low,
 yellow_high, red_high, green_low, green_high]`.
- Limits states: `RED_LOW`, `YELLOW_LOW`, `GREEN`, `YELLOW_HIGH`,
 `RED_HIGH`, plus `BLUE` for the optional operational band.
- Streaming API (Go only, optional): topic
 `<MODE>__<CMD|TLM>__<TARGET>__<PACKET>`, items `…__<RAW|CONVERTED|FORMATTED>__`.
- Channel naming: `<TARGET>__<PACKET>__<POINT>`; the runner also accepts the
 `<PACKET>__<POINT>` shorthand when the suite declares a target.
- Authentication: the mock is keyless. Against a real COSMOS the client
 sends no token by default and requires no `/auth/verify` round trip for local
 runs (see `--token` for deployments that need one).

## 1.4 Telemetry data model

Three value domains appear everywhere and are not interchangeable:

| Domain | Name | Meaning |
| --- | --- | --- |
| `DN` | raw / digital number | integer counts as they arrive on the wire |
| `EU` | converted / engineering units | physical value after the POLY conversion |
| formatted | display string | for humans, never for assertions |

Every point is defined by:

```python
TelemetryPoint(
    target="DROID_SIM", packet="THERMAL", point="RW_TEMP",
    units="C",
    dn_min=2000, dn_max=11000,            # raw wire range
    eu_min=-20.0, eu_max=70.0,            # converted range
    conversion="POLY", coeffs=(-40.0, 0.1),  # EU = -40 + 0.1*DN
    limits=Limits(red_low=-5, yellow_low=10, yellow_high=45, red_high=55,
                  green_low=10, green_high=45,
                  blue_low=10, blue_high=45),
    limits_enabled=True,
)
```

Limits evaluation order (this is the part people get wrong):

1. If limits are disabled to `GREEN`.
2. `value <= red_low` to `RED_LOW`; `>= red_high` to `RED_HIGH`.
3. `value <= yellow_low` to `YELLOW_LOW`; `>= yellow_high` to `YELLOW_HIGH`.
4. Otherwise the point is safe; then and only then the BLUE band decides
 presentation: inside `[blue_low, blue_high]` to `BLUE`, else `GREEN`.

So BLUE is *never* a violation and *never* masks YELLOW or RED (asserted by
`TestBlueBandNeverMasksYellowOrRed` in Go and by the equivalent Python tests).
`is_violation(state)` is true only for the four YELLOW/RED variants.

System rollup: the worst point state wins; `demo` asserts the wheel's
`YELLOW_HIGH` rolls SYS up to YELLOW while the radiator stays healthy.

## 1.5 Target definition (DROID_SIM)

`definitions/droid_sim/cmd_tlm/droid_sim_tlm.txt` and `droid_sim_cmds.txt` use the
COSMOS text syntax
(`TELEMETRY <TARGET> <PACKET> <ENDIAN> <item> …`), so they can be dropped into a
real COSMOS target directory unchanged. The same target definition is bundled
into the wheel (`cosmos_tlm_bridge/_bundled/…`), which is why `demo` works from
any installation with no repository checkout.

Contents (verify with `cosmos-tlm-bridge targets`): 3 packets × 9 typed
points = 27 typed points, 12 of which are real signals (the other 5 per
packet are the CCSDS header items `CCSDS_STREAMID`, `CCSDS_SEQUENCE`,
`CCSDS_LENGTH`, `CCSDS_SECONDS`, `CCSDS_SUBSECONDS`), 9 points carry limits
bands, and there are 2 commands (`CMD_SLEW_TARGET`, `CMD_SET_LIMITS`).

| Packet | Point | Units | EU range | BLUE (op) band | Conversion |
| --- | --- | --- | --- | --- | --- |
| ADCS | `ATTITUDE_ERROR` | deg | −10 … 10 | [−2, 2] | `0 + 0.001x` |
| ADCS | `RW_SPEED` | rpm | −6000 … 6000 |. | `0 + 0.1x` |
| ADCS | `SLEW_ACTIVE` | flag | 0 … 1 |. | raw UINT 8, no conversion or limits (asserted by suites) |
| ADCS | `SLEW_TARGET_ID` | id | 0 … 3 |. | raw UINT 8, no conversion or limits (asserted by suites) |
| EPS | `BATT_VOLTAGE` | V | 13.9 … 17.1 | [15, 16] | `13.6 + 0.0025x` |
| EPS | `BATT_CURRENT` | A | −10 … 6 |. | `−8 + 0.01x` |
| EPS | `BATT_SOC` | pct | 5 … 100 | [40, 90] | `0 + 0.01x` |
| EPS | `BATT_TEMP` | C | −20 … 55 | [5, 35] | `−40 + 0.01x` |
| THERMAL | `RADIATOR_TEMP` | C | −35 … 60 | [−15, 35] | `−40 + 0.01x` |
| THERMAL | `RW_TEMP` | C | −20 … 70 | [10, 45] | `−40 + 0.01x` |
| THERMAL | `BUS_TEMP` | C | −30 … 55 | [−5, 30] | `−40 + 0.01x` |
| THERMAL | `HEATER_DUTY` | pct | raw UINT 16 |. | raw counts, no limits |

Run `.venv/bin/cosmos-tlm-bridge limits` to print this table from the live
definitions (never hand-copied into docs).

The simulated plant (`mock/droid.py`) integrates a simple LEO pass: battery
charge/discharge, radiator response, and a commanded slew that heats the
reaction wheel into `YELLOW_HIGH` and back out again.

## 1.6 The bridge

`bridge.py` subscribes to packets through `client.py` and emits
`TelemetryFrame` objects:

```json
{
  "target": "DROID_SIM", "packet": "THERMAL", "received_at": "2026-11-03T…Z",
  "seq": 42,
  "points": [
    {"point": "RW_TEMP", "dn": 9000, "eu": 50.0, "units": "C",
     "limits_state": "YELLOW_HIGH", "limits_enabled": true, "violation": true}
  ],
  "summary": {"counts": {"BLUE": 1, "GREEN": 0, "YELLOW_HIGH": 1},
              "worst": "YELLOW_HIGH", "violation": true}
}
```

Design points:

- `--dry-run` intends to validate wiring without a consumer.
- A webhook sink (`WebhookSink`) POSTs each frame with a bounded retry
 policy; exhausted retries are counted as `webhook_fail`, never silently
 dropped.
- `PointSubscription` filters by target/packet/point and by
 `only_violations`, so a downstream service can subscribe to just the signals
 it needs.
- Counters (`frames`, `points`, `webhook_ok`, `webhook_fail`) stay available for
 scraping or test assertions.

## 1.7 Acceptance suites: YAML reference

A suite is a Pydantic model (`extra="forbid"`, so typos fail fast).

```yaml
suite: droid_pass_acceptance      # required
description: >
  Simulated LEO pass.
target: DROID_SIM                 # default DROID_SIM
scope: DEFAULT                    # COSMOS scope
timeout_s: 105                    # whole-suite budget
tags: [acceptance, thermal]
fresh_sim: true                   # reset the plant first (no-op on real COSMOS)
packets: [ADCS, EPS, THERMAL]     # subscribed packets
webhooks: []                      # optional sink URLs checked during the run

setup:
  - name: all packets are streaming
    wait_for_packet: EPS
    within_s: 20

steps:
  - name: slew the vehicle
    cmd: CMD_SLEW_TARGET
    check: ENABLED                # ENABLED | NO_CHECK
    settle_s: 0.5

  - name: wheel goes yellow in EU
    assert:
      - channel: THERMAL__RW_TEMP
        dn_eu: EU
        expect_limits: YELLOW_HIGH
        within_s: 40
        occurrence: ANY           # ANY | EVER | ALWAYS

  - name: bus voltage inside the operational band
    assert:
      - channel: EPS__BATT_VOLTAGE
        dn_eu: EU
        condition: between 15 and 16
        within_s: 20

  - name: wait 2 s
    wait_s: 2
    reason: let the radiator settle

teardown:
  - name: bus voltage still nominal at end of pass
    assert:
      - {channel: EPS__BATT_VOLTAGE, dn_eu: EU, condition: "> 14.0"}
```

Step kinds (exactly one per step, detected by its key):

| Key | Model | Behaviour |
| --- | --- | --- |
| `cmd` | `CommandStep` | issue the command; `check: ENABLED` requires the command to be enabled first; then wait `settle_s` |
| `assert` | `AssertStep` | one or a list of `AssertionSpec`; `require_all: true` by default |
| `wait_s` | `WaitStep` | sleep `wait_s` seconds |
| `wait_for_packet` | `WaitForPacketStep` | block until a packet arrives, up to `within_s` |
| `wait_for_limits` | `WaitForLimitsStep` | block until any point on `packet` enters the state, up to `within_s` |

`AssertionSpec` fields

| Field | Default | Notes |
| --- | --- | --- |
| `channel` | required | `TARGET__PACKET__POINT` or `PACKET__POINT` |
| `dn_eu` | `EU` | `DN` asserts raw counts, `EU` asserts converted values |
| `condition` |. | `"> 14.0"`, `"between 15 and 16"`, `"outside [0,1]"`, `"change > 0.5"`, `"~ 14.0 ± 0.05"` |
| `expect_limits` |. | `GREEN`, `BLUE`, `YELLOW_LOW`, `YELLOW_HIGH`, `RED_LOW`, `RED_HIGH` |
| `expect_limits_color` |. | same set, colour-style alias |
| `within_s` | `20.0` | deadline for the assertion |
| `occurrence` | `ANY` | `ANY` = at least one matching sample; `ALWAYS` = every sample; `EVER` = must appear at some point and is checked at the end |
| `sample_interval_s` | `0.15` | polling cadence |

At least one of `condition` / `expect_limits` / `expect_limits_color` is
required. a bare channel reference is rejected at load time.

Comparisons: `>`, `>=`, `<`, `<=`, `between a and b`, `outside [a,b]`,
`change > x` (needs a prior sample), `~ value ± tolerance`. Limits assertions
never hard-code thresholds: they read the live limits table from the target
definition.

## 1.8 Regression runner semantics

- Setup steps run first, then the steps, then teardown. teardown runs even
 after a failure so the vehicle is left in a known state.
- Every step is bounded by its own `within_s` and by the suite `timeout_s`;
 the suite budget is enforced with a deadline, not a sum of sleeps.
- One `CaseResult` per named check: `name`, `status` (`PASS`/`FAIL`/`ERROR`/
 `SKIP`), `detail`, timing. Exit code is 1 if any case failed or errored.
- A negative control lives in the test suite: a copy of the thermal suite that
 expects `RED_HIGH` where only `YELLOW_HIGH` occurs must fail. This proves
 the assertions can detect a missing violation instead of trivially
 passing.
- `fresh_sim: true` restarts the simulated plant; without it, runs inherit the
 previous vehicle state (realistic for a live pass, non-deterministic for CI).

## 1.9 Reports and artifacts

Per run, into `--out` (default `artifacts/`):

| File | Content |
| --- | --- |
| `<suite>-junit.xml` | JUnit XML. one `testcase` per check, `failure`/`error` elements, suite timing; consumable by Jenkins/GitLab/GitHub |
| `<suite>-report.html` | Self-contained HTML report: summary banner, per-suite timing budget, step table with colours, failure details |
| `<suite>-results.json` | Machine-readable results: suite name, status, counts, duration, per-case fields |

Ship-ready artifacts are already in `artifacts/` for all three suites plus an
earlier `droid_pass` run.

## 1.10 CLI reference

`cosmos-tlm-bridge` and `cosmos-regress` are the same program (both entry
points map to `cosmos_tlm_bridge.cli:main`); the verb is `run`, and there is no
`regress` subcommand.

| Verb | Purpose | Key flags |
| --- | --- | --- |
| `demo` | self-contained acceptance run: start mock, run a suite, write reports | `--suite`, `--out`, `--port` |
| `run` | run a suite against an already-running COSMOS | `SUITE`, `--out`, `--url`, `--token` |
| `serve` | start the keyless DROID_SIM reference COSMOS | `--port` (default 2900), `--scope` |
| `bridge` | stream typed frames to a webhook | `--packet`, `--duration`, `--webhook`, `--dry-run`, `--only-violations` |
| `verify` | print channel values in DN and EU with limits state (human check) | `--packet`, `--point` |
| `limits` | print the typed limits table |. |
| `targets` | list typed channels for the target | `--target` |
| `version` | print version |. |

Exit codes: `0` success, `1` suite failure / assertion error, `2` usage error
(argparse), `3` COSMOS unreachable.

`cosmos-tlm-bridge verify` is the fastest way to see that DN and EU are both
live and that the limits engine is working:

```
$ cosmos-tlm-bridge verify --packet THERMAL
THERMAL__RW_TEMP        DN 9000      EU 50.000 C   YELLOW_HIGH
```

## 1.11 Go implementation

`go/` is an independent client + bridge, useful for services written in Go that
must consume the same telemetry. Module `github.com/turion-space/cosmos-tlm-bridge/go`,
no third-party dependencies.

- `types.go`. `TelemetryPoint`, `Limits`, `LimitsState`, POLY conversion,
 `IsViolation`, channel helpers, streaming topic/item naming.
- `client.go`. JSON-RPC over HTTP (`tlm`, `tlm_raw`, `cmd`, `get_limits`,
 streaming item keys) with scope handling and structured errors.
- `bridge.go`. `TelemetryBridge`, `PointSubscription`, `WebhookSink`
 (bounded retries, counters, `FrameSummary` that collapses BLUE points).
- `cmd/tlm-bridge/main.go`. a small runnable CLI.

The Go limits precedence is asserted to match Python bit-for-bit
(`TestPolyConversionMatchesPython`, `TestBlueBandNeverMasksYellowOrRed`,
`TestLimitsPrecedence`, `TestLimitsDisabled`). the two implementations must not
drift.

## 1.12 Docker / compose

- `Dockerfile`. multi-stage, installs the wheel, runs `cosmos-regress` as the
 entry point.
- `docker-compose.yml`. COSMOS reference server, the bridge, and a webhook
 sink (`docker/sink.py`) that prints received frames.
- `docker/entrypoint.sh`. waits for COSMOS, then runs the requested suite.

Status: authored, never built (no Docker available where this was
developed). Treat compose as "ready to run", not "proven".

## 1.13 Tests

`tests/test_bridge_and_regress.py` and `tests/test_models_and_limits.py`, 80
tests, `pytest`, no network, no keys:

- models/limits: conversion round-trips, limits precedence, BLUE never masks
 YELLOW/RED, disabled limits, severity rollup.
- client: JSON-RPC shape, error mapping, unreachable-server behaviour.
- mock: packet streaming, command handling, slew-induced thermal violation.
- bridge: subscription filtering, `only_violations`, webhook delivery and retry
 exhaustion.
- runner: suite loading/validation, positive run, negative control (wrong
 expectation must fail), timeout behaviour, teardown-after-failure.

Run: `.venv/bin/python -m pytest -q` to `80 passed`.

## 1.14 Configuration and limits

| Concern | Value / behaviour |
| --- | --- |
| COSMOS URL | `--url`, default `http://localhost:2900`; scope `DEFAULT` |
| Credentials | none (mock is keyless); `--token` exists for secured deployments |
| Instance caps | intents bounds on max points per frame, max suites, max duration (see `models.py`); enforced, also documented |
| Runtime budget | full acceptance run < 45 s (measured 22.8 s) |
| Python | ≥ 3.11, deps: `pydantic>=2.6,<3`, `PyYAML>=6.0,<7` |
| Go | any modern toolchain, zero external modules |
| Not supported | real COSMOS container (no Docker here), command verification beyond `ENABLED`, binary/CCSDS framing (COSMOS handles it) |

---

# 2. Project 2: turion-platform-drift

Folder: `PROJECT-2-turion-platform-drift/`. Module name `turion-platform-drift`,
Go 1.22+, zero external dependencies.

## 2.1 Purpose and problem

Turion's ground service is welded to AWS: boto3 clients for Batch, Lambda and S3,
IRSA annotations, ECR image references, EBS storage classes, CloudWatch logging.
A ground station in the field has none of it. Rewriting by hand is guesswork
because nobody knows the full list of couplings.

The tool answers three questions, in order:

1. Where is the lock-in?. AST-based detection across Go, Python,
 TypeScript and manifests, 26 rules, with file/line/column and a portable
 replacement for each finding.
2. How bad is it?. a score (0-100, A-F) with severity weighting,
 per-category breakdown, hotspots and a CI gate.
3. What does the portable version look like?. a generated bundle that
 applies to a bare cluster, plus optional Crossplane/Helm/OTel manifests.

## 2.2 Architecture

```
 main.go ──► cli: reorderArgs → scan | generate | validate | rules | version
   │
   ├─► internal/scan   — file discovery, language classification, collectors
   │      ├─ golang.go     go/ast: imports, call sites, literals, annotations
   │      ├─ analyzer.py   Python `ast` helper (embedded, run via python3)
   │      ├─ manifests.go  YAML/JSON/HCL/Terraform/Helm/AWS policy analysis
   │      └─ findings.go   Finding model, dedupe/merge, severity
   ├─► internal/rules  — the 26-rule catalog (id, severity, category,
   │                     remediation, portable replacement)
   ├─► internal/score  — weights → saturating penalty → score, grade, verdict,
   │                     categories, by-rule, hotspots
   ├─► internal/generate — target-aware templates (kubernetes | crossplane),
   │                     naming, namespace, registry rewrite, splitting
   ├─► internal/validate — embedded schema checks, kubectl dry-run (client and
   │                     server), residual-AWS scan, Helm rendering
   ├─► internal/report — text, JSON, SARIF 2.1.0, HTML scorecard, rendering
   └─► internal/yamlite — tolerant YAML subset parser used where a full YAML
                          library is unavailable (no cgo, no modules)
```

Why `yamlite`: k8s manifests contain multi-document streams, block scalars,
anchors (rejected with a clear error), comments and quoting styles. With a
strict zero-dependency constraint the choice was a small tolerant parser plus a
fuzzer that has run >1.5 M executions. It is used for *inspection*, never for
round-tripping a file.

## 2.3 Scan pipeline

1. Discover. walk the root, skipping `.git`, `node_modules`, `vendor`,
 `dist`, `build`, `__pycache__`, `.venv`, `bin`, and any `--exclude` substring.
 Symlinks are not followed; unreadable files become recoverable errors.
2. Classify. extension to language: `.go`, `.py`, `.ts/.tsx`, `.js`,
 `.yaml/.yml`, `.json`, `.tf/.tfvars/.hcl/.nomad/.jsonnet/.libsonnet`, `.sh`,
 `.toml`, `.txt`.
3. Analyse. per language:
 - Go: `go/parser` + `go/ast`; imports, selector calls, string literals,
 struct/annotation values.
 - Python: `internal/scan/analyzer.py` invoked once per run with the file
 list; it emits JSON findings (`boto3.client("x")`, `boto3.resource`,
 `@boto3` imports, `arn:aws`, `s3://` literals, IAM policy dicts). If
 `python3` is missing it returns a structured "unavailable" marker. never an
 empty success. and `--no-python` switches to regex fallback.
 - TypeScript/JS: import + call-site patterns (`@aws-sdk/*`, `new XClient`,
 `batch.submitJob`, …).
 - Manifests: YAML/JSON/HCL walkers detect Batch job definitions and
 compute environments, ECR images, IRSA annotations, EBS/EFS storage
 classes, ALB annotations, `aws_*` Terraform resources, Secrets Manager CSI
 volumes, Karpenter `EC2NodeClass`, CloudWatch agent config.
 - AWS policy documents: `.json` files under an `iam/`/`policy` path or
 containing an IAM statement document, plus anything named `*trust*`
 (`IsAWSPolicyPath` is `.json`-only and trust-aware. a plain `.txt` is not
 a policy).
4. Deduplicate. same rule on same line collapses when symbol spans overlap;
 distinct call sites keep separate findings so the count stays honest.
5. Report. `scan.Result` to `score.Build` to renderer.

Guarantees, each backed by a test:

- Zero false negatives on the fixture: every one of the 26 rules fires on
 `ground-service/` (`TestOneProbePerRuleFires` uses `probes/tpdNNN/` so
 per-file "best rule wins" cannot mask a miss).
- Zero false positives on portable code: `testdata/clean-service/` (Go,
 Python, TypeScript, YAML) produces no findings
 (`TestCleanServiceIsSilent`).
- Read caps: `MaxReadBytes = 8 MiB` per file; oversized files are reported, not
 scanned partially.
- Adversarial input (15 hostile files) yields 2 findings + 6 recoverable errors
 and no panic; every collector recovers per file so one bad file cannot
 abort a scan.

## 2.4 Rule catalog (26 rules)

`go run turion-platform-drift rules` prints this table from
`internal/rules/rules.go` (the doc never drifts from the code).

| ID | Sev | Category | Detects | Portable replacement |
| --- | --- | --- | --- | --- |
| TPD001 | critical | COMPUTE | boto3 / botocore import | object-store shim + typed config |
| TPD002 | critical | COMPUTE | boto3 client bound to a proprietary service | Kubernetes Job / Crossplane composite |
| TPD003 | critical | SERVERLESS | AWS Lambda invocation | Kubernetes Job + ClusterIP Service |
| TPD004 | critical | COMPUTE | AWS Batch job submission | Kubernetes Job manifest |
| TPD005 | high | OBJECT-STORAGE | hard-coded S3 bucket / ARN literal | MinIO / VersityGW bucket + ConfigMap |
| TPD006 | high | OBJECT-STORAGE | proprietary S3 primitive usage | S3-compatible client factory |
| TPD007 | critical | IDENTITY | `arn:aws` literal in source | Crossplane composite + ConfigMap |
| TPD008 | critical | IDENTITY | IAM / STS role assumption | ServiceAccount + workload identity |
| TPD009 | critical | IDENTITY | non-portable IAM policy document | RBAC + MinIO policy |
| TPD010 | high | MESSAGING | SQS/SNS/Kinesis usage | NATS JetStream / Redpanda |
| TPD011 | high | DATABASE | RDS/DynamoDB/Aurora usage | CloudNativePG PostgreSQL |
| TPD012 | high | COMPUTE | AWS CLI invocation of a proprietary service | kubectl / Helm / Crossplane |
| TPD020 | critical | COMPUTE | `aws-sdk-go` / `-v2` import | S3-compatible client factory |
| TPD021 | critical | SERVERLESS | AWS SDK service client in Go | Kubernetes Job / object-store shim |
| TPD030 | critical | COMPUTE | `@aws-sdk/*` import | S3-compatible client + HTTP |
| TPD031 | critical | SERVERLESS | AWS SDK Lambda client usage | Kubernetes Service |
| TPD040 | critical | COMPUTE | AWS Batch job definition manifest | Kubernetes Job manifest |
| TPD041 | critical | COMPUTE | AWS Batch compute environment | NodePool / PriorityClass |
| TPD042 | critical | REGISTRY | Amazon ECR image reference | Harbor / Zot registry |
| TPD043 | critical | IDENTITY | IRSA annotation `eks.amazonaws.com/role-arn` | ServiceAccount + Secret |
| TPD044 | high | NETWORK | AWS Load Balancer Controller ingress | NGINX Ingress / Gateway API |
| TPD045 | high | OBJECT-STORAGE | EBS/EFS storage class | Longhorn / Ceph RBD |
| TPD046 | critical | CONFIG | Secrets Manager / SSM CSI volume | Kubernetes Secret / ESO + Vault |
| TPD047 | high | COMPUTE | Karpenter `EC2NodeClass` | NodePool + Cluster Autoscaler |
| TPD048 | high | OBSERVABILITY | CloudWatch Logs / X-Ray | OpenTelemetry Collector |
| TPD049 | critical | COMPUTE | `aws_*` Terraform/CloudFormation resource | Crossplane composite |

Severities are contractual: `critical` means the workload cannot start without
AWS.

## 2.5 Scoring model

```
weight:  critical = 12, high = 7, medium = 3, low = 1
raw     = Σ weight(finding)                       (per finding, not per rule)
penalty = 100 × (1 − e^(−raw / 45))               (diminishing returns)
score   = round(100 − penalty), clamped to [0, 100]
```

Grades: A ≥ 90, B ≥ 75, C ≥ 55, D ≥ 30, F < 30. The
saturating curve means ten copies of the same call do not drive the score to
zero, while a single critical coupling already costs ~23 points. which matches
the operational truth that one boto3 Batch call is enough to block a deployment.

The scorecard carries: root, score, grade, verdict, file count, severity counts,
distinct rules, per-category penalties (sorted), per-rule buckets with
remediation, language histogram, duration, whether the Python AST pass ran, top
hotspots and any recoverable errors.

Reference result on `ground-service/`: 112 findings (80 critical, 32 high),
26/26 rules, score 0/100, grade F, 0.04 s.

## 2.6 Generator

`scan --generate --out DIR [--target kubernetes|crossplane]` writes:

| Path | Content |
| --- | --- |
| `ground-station-portable.yaml` | the whole bundle, multi-document |
| `kustomize/kustomization.yaml` | overlay that can `kubectl apply -k` the bundle |
| `scorecard.json`, `scorecard.html` | the scan result |
| `drift.sarif` | SARIF 2.1.0 for code scanning |
| with `--split` | one file per object instead of a bundle |

`--target kubernetes` (default). built-in API kinds only. Nothing here
needs an operator or a CRD, so it applies to a bare cluster. The bundle is 865
lines / 27 objects (object names verbatim from the generated file):

| Replaces | Objects |
| --- | --- |
| (cluster scaffolding) | `Namespace ground-system` |
| S3 bucket + endpoint | `Secret objectstore-credentials`, `ConfigMap objectstore-config`, `PVC versitygw-data`, `Deployment versitygw`, `Service objectstore`, `ConfigMap objectstore-buckets`, `Job objectstore-bucket-bootstrap` (creates buckets with `curl --aws-sigv4`. no SDK, no CRD) |
| CloudWatch / X-Ray | `ConfigMap otel-collector`, `Deployment otel-collector`, `Service otel-collector` (OTLP in, Prometheus/Loki out) |
| IRSA / IAM | `ServiceAccount ground-service`, `Secret ground-service-credentials`, `Role ground-service`, `RoleBinding ground-service` |
| DynamoDB / RDS | `Secret ground-telemetry-db`, `PVC ground-telemetry-db`, `StatefulSet ground-telemetry-db` (PostgreSQL), `Service ground-telemetry-db` |
| Secrets Manager / SSM | `Secret ground-service-secrets` |
| Lambda + API Gateway | `Deployment telemetry-enrich`, `Service telemetry-enrich` |
| SQS / SNS | `StatefulSet nats`, `Service nats` |
| AWS Batch | `Job cronjob`, `Job droid-pass-processor`, `Job submit-pass-processing` (`backoffLimit`, `ttlSecondsAfterFinished`, resources, node affinity) |

All 12 distinct kinds are built-in (`Namespace`, `ConfigMap`, `Secret`,
`ServiceAccount`, `Role`, `RoleBinding`, `PVC`, `Deployment`, `StatefulSet`,
`Service`, `Job`). no CRD appears anywhere in this target. Pod security
contexts satisfy the restricted Pod Security Standard, and image references are
rewritten to `registry.ground.local/<name>:<tag>` (`--registry` to change).
This bundle really applies: `27 created / 0 warnings / 0 errors` against a
fresh API server.

`--target crossplane`. operator layer. The same workload plus the
declarative infrastructure: `XRD` + `Composition` (v2 Pipeline composition.
`pipeline: - step: … function:` style, not the deprecated v1 patches-only form),
`ProviderConfig` for the object store (`minio.crossplane.io/v1`), a composite
claim (`templates/crossplane/groundstack-claim.yaml`) that asks for a
`Bucket`, plus CNPG `Cluster`, ESO `SecretStore`/`ExternalSecret` and an OTel
`OpenTelemetryCollector`. Every CRD was fetched from its upstream release and
installed in the verification cluster, so this bundle is admitted by a real
API server, also linted.

Hard rule: both bundles contain zero AWS *identifiers*. no `arn:aws`, no
`*.amazonaws.com`, no `s3://`, no IRSA annotation. so
`grep -rE "arn:aws|\.amazonaws\.com|s3://|eks\.amazonaws\.com"` over the output
is a valid audit. The literal word "AWS" does appear, but only in explanatory
comments and provenance annotations such as `ground.turion.space/replaces:
aws-s3` and "no AWS endpoint", which document *what was replaced*. Precise
wording matters here: the bundles are free of AWS *references*, not of the word.
`--fail-on-residual-aws` and `scripts/verify.sh` enforce the identifier gate on
every run.

## 2.7 Validator

`validate <dir>` runs, in order:

1. Structure/schema gate. YAML parses, every document has `apiVersion`,
 `kind`, `metadata.name`; names are DNS-1123; namespaces and container specs
 are consistent; images are not `:latest` where a digest/registry is required.
2. kubectl client dry-run. `kubectl apply --dry-run=client -f <bundle>`
 (modern kubectl validates against the server's OpenAPI, so this is a real
 schema check; "no matches for kind" arrives on stderr and is captured).
3. kubectl server dry-run. `--dry-run=server` when a cluster is reachable:
 admission, defaulting and CRD schema validation run.
4. Residual AWS scan. any AWS identifier left in the generated output.
5. Helm rendering. `helm template` on the chart under
 `templates/helm/ground-service/` when `helm` is present.

Honesty rule: an unavailable external tool produces
`skipped (reason: …)`, never a silent pass. `--require-kubectl` /
`--require-helm` promote a skip into a failure, and CI runs with the requiring
flags.

## 2.8 Reports

- text. human scorecard: header, score bar, grade, severity table,
 per-category penalties, by-rule table with remediation, top hotspots.
- JSON. the full `Scorecard` struct (`scorecard.json`), stable field names
 for automation.
- SARIF 2.1.0 (`drift.sarif`). rules with `helpUri`/`help.text`, results
 with `level` (error for critical, warning for high), physical locations and
 `partialFingerprints`; uploaded by CI to GitHub code scanning.
- HTML (`scorecard.html`). self-contained, no external assets (works
 offline and inside a sandboxed preview).

## 2.9 CLI reference

```
turion-platform-drift <command> [flags]

COMMANDS
  scan <path>        Analyse a repository and print the portability scorecard
  generate <path>    Scan, then emit portable replacement manifests
  validate <dir>     Validate generated manifests (air-gapped, no cluster needed)
  rules              List every detection rule and its portable replacement
  version            Print the version
  help               Usage

FLAGS
  --format text|json|sarif     output format for scan
  --out <dir>                  reports and generated manifests
  --generate                   generate replacements as part of scan
  --target kubernetes|crossplane
  --namespace <ns>             default ground-system
  --object-store versitygw|minio
  --registry <host/path>       default registry.ground.local
  --split                      one file per object
  --exclude <substr>           repeatable path exclusion
  --no-python                  skip the Python AST pass (regex fallback)
  --top <n>                    hotspots to print (default 8)
  --kubeconfig <path>          cluster for server dry-run / apply checks
  --require-kubectl            a kubectl skip becomes a failure
  --server-dry-run             use --dry-run=server
  --helm / --no-helm / --require-helm
  --fail-under <score>         exit 1 when score < N
  --fail-on-critical           exit 1 when any critical finding exists
  --fail-on-any                exit 1 when any finding exists
  --fail-on-residual-aws       exit 1 when generated output still references AWS
```

Exit codes: `0` success, `1` gate failure, `2` usage error.

Flags may appear before or after the positional path (`reorderArgs`
normalises argv; `boolFlags` must list any new valueless flag).

## 2.10 Verification harness

### `scripts/verify.sh`: 32 gates in 7 sections

| Section | Gates |
| --- | --- |
| Hygiene | `gofmt -l` empty, `go vet` clean, build succeeds, no `require` lines in `go.mod` |
| Correctness | `go test ./...`, `-race`, one probe per rule, portable corpus silent |
| Stress / adversarial / determinism / idempotency | 3,000-file repo, 15 hostile files, byte-identical repeat runs, re-generation is a no-op diff |
| Fuzzing | `yamlite`, manifest scanner, Go AST (each with a time budget) |
| DoD scan | the exact DoD command, ≥26 distinct rules, score/grade as expected |
| Generation | both targets, 27 objects, schema validation, built-in-kinds grep, zero-AWS grep, residual-AWS gate, Helm render |
| Cluster | CRD install (45 CRDs), client and server dry-run for both bundles, two real applies, `--require-kubectl --server-dry-run` |

`--no-cluster` skips section 7. Full run: 32 passed, 0 failed, ~92 s. including from a machine with no cluster, no `kubectl`, no `helm` and no `kwokctl`, which the scripts install themselves.

### `scripts/kwok-cluster.sh up|down|status`

Brings up etcd + kube-apiserver (kwok binary runtime, no Docker, no kubelet)
with `--extra-args kube-apiserver=advertise-address=127.0.0.1`; prints a
kubeconfig to `.kwok/clusters/ground/kubeconfig.yaml`. Used by the cluster gates
and by CI.

### `scripts/install-crds.sh`

Installs the 45 CRDs: Crossplane `v2.4.3` `cluster/crds` (21 files),
provider-minio `v0.4.4` `package/crds` (2 files), and the CNPG / External
Secrets / OpenTelemetry Operator chart CRDs from the cached Helm repos.

### `Makefile`

`build test race stress fuzz bench cover scan generate validate cluster crds
verify verify-fast ci demo clean help`. `make verify` runs the 32 gates;
`make verify-fast` runs the no-cluster subset.

### `.github/workflows/ci.yml`

Four jobs: `build-test` (fmt/vet/build/test/race), `accuracy`
(one-probe-per-rule, portable corpus silent), `fuzz` (three targets),
`portability-gate` (kwok cluster, CRD install, both targets, real apply,
client + server dry-run, `--require-kubectl`, SARIF upload, artifact upload).

### Test layout

| File | Scope |
| --- | --- |
| `scan_test.go` | end-to-end scan of the fixture, counts, rule coverage |
| `accuracy_test.go` | one probe directory per rule (`probes/tpdNNN/`), portable corpus silent |
| `brutal_test.go` | stress (3,000 files), adversarial inputs, determinism, idempotency, encoding edge cases |
| `fuzz_test.go` + `internal/yamlite/fuzz_test.go` | fuzzing entry points and the regression corpus |
| `bench_test.go` | `BenchmarkScanGroundService` ~43 ms/op, `BenchmarkScanManifest` ~102 µs, `BenchmarkYAMLiteParseBundle` ~416 µs, `BenchmarkGenerate` ~164 µs |
| `cli_test.go` | every command, exit codes, flags after positional, report files, gate flags |
| `internal/*/…_test.go` | unit tests per package, including `scan_internal_test.go` (`langFor`, exclusions, dedupe/merge, `ReadFile` cap, `IsAWSPolicyPath`, no-Python fallback, bad Go source) |

99 tests, 84.3 % statement coverage (`-coverpkg=./...`).

---

# 3. Cross-cutting conventions

- Names are contractual. `cosmos-tlm-bridge`, `turion-platform-drift`,
 `cosmos-regress`, `/__openc3_api__/api`, `ground-service`,
 `ground-station-portable.yaml`, `scorecard.json`, `drift.sarif`. Do not rename
 without updating the DoD, the docs and `scripts/verify.sh` together.
- No secrets, no keys, no cloud credentials anywhere in either project. The
 Python package needs no OpenC3 SDK; the Go module needs no third-party module.
- Honest reporting. A capability that was not exercised is labelled
 *authored, not proven*, and an external tool that is missing yields
 `skipped (reason)`, never a pass. `HANDOVER.md` §3 keeps the list.
- Evidence over narrative. Any claim in the docs should point at a command
 or an artifact that can be re-run.
