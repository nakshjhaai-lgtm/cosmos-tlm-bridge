# cosmos-tlm-bridge

OpenC3 COSMOS v5 to typed ground-microservice bridge and declarative CI regression runner.
Built for [Turion Space](https://turion.space) DROID constellation operations.

Keyless and no OpenC3 SDK and no Ruby Script Runner and no API keys and Python 3.11+ / Go / Docker.

```bash
uvx cosmos-tlm-bridge demo                      # full DROID pass acceptance in ~23 s
docker compose up --build                       # same stack, three containers
cosmos-regress run droid_pass_acceptance.yaml   # CI gate, JUnit + HTML out
```

---

## Why this exists

Turion is scaling from single-satellite missions to DROID constellations. Operations
and ground software engineers drive command-and-control through OpenC3 COSMOS v5,
and every satellite software update or new mission concept currently means hand-writing
brittle Ruby/Python procedures in OpenC3 Script Runner that:

* manually correlate raw Data Numbers (DN) against converted Engineering Units (EU),
* hand-reimplement the COSMOS limits algorithm (`YELLOW_HIGH`, `RED_HIGH`, BLUE operational bands),
* and drift out of sync with the target definitions the moment an item changes.

`cosmos-tlm-bridge` replaces that loop with three things:

1. a typed bridge that decommutates COSMOS JSON-RPC / StreamingApi telemetry into
 Pydantic and Go structs, converting DNtoEU from the *same* `cmd_tlm/*.txt` definitions
 COSMOS loads, and evaluating operational limits states itself;
2. a declarative YAML acceptance runner so regression suites are data, not scripts;
3. a `DROID_SIM` reference server so all of it runs keyless, in CI, in under a minute.

No Ruby is written or executed anywhere in the pipeline.

---

## Quick start

### 1. One-command demo (no Docker, no COSMOS)

```bash
uvx cosmos-tlm-bridge demo
```

Boots the reference server, runs `droid_pass_acceptance.yaml`, writes JUnit + HTML +
JSON reports to `artifacts/`, and fails the process on any regression. in ~23 s,
inside the 45 s spin-up budget.

```
1/4 booting DROID_SIM OpenC3 reference server ... ready (0.01s)
2/4 verifying JSON-RPC + typed decommutation ... ok (9 points on EPS)
3/4 running acceptance suite droid_pass_acceptance
  PASS [setup] all packets are streaming
  PASS [steps] command the attitude slew
  PASS [steps] slew is acknowledged in telemetry (DN mode)
  PASS [steps] reaction wheel thermal limit violation is caught (EU mode)
  PASS [steps] reaction wheel thermal limit violation is caught (DN mode)
  ...
  -> PASS 15 passed, 0 failed, 0 errors
4/4 writing reports ... done

instant spin-up  22.7s / 45.0s PASS
```

### 2. Docker Compose

```bash
docker compose up --build           # cosmos + bridge + telemetry-sink
docker compose --profile verify up  # + one-shot acceptance gate
```

| Service | Port | Role |
| --- | --- | --- |
| `cosmos` | 2900 | Mock OpenC3 COSMOS v5: JSON-RPC, auth/verify, StreamingApi |
| `bridge` | - | Typed fan-out from COSMOS to microservice webhooks |
| `telemetry-sink` | 8080 | Downstream ground service consuming typed frames |
| `regress` | - | CI gate; mounts `./artifacts` |

### 3. Against a real COSMOS

```bash
export COSMOS_URL=https://cosmos.ground.turion.internal
export COSMOS_SCOPE=DEFAULT
export COSMOS_AUTH_MODE=core          # keyless | core | enterprise
export COSMOS_PASSWORD=...            # from your secret manager

cosmos-regress run suites/droid_pass_acceptance.yaml --out artifacts
cosmos-tlm-bridge verify DROID_SIM__THERMAL__RW_TEMP --limits
```

---

## Architecture

```
            ┌──────────────────────────────────────────────────────────────┐
            │  OpenC3 COSMOS v5  (real instance, or bundled DROID_SIM)      │
            │                                                              │
            │  POST /openc3-api/api            JSON-RPC 2.0                │
            │  POST /__openc3_api__/api        JSON-RPC 2.0 (proxied)      │
            │  POST /openc3-api/auth/verify    COSMOS Core password        │
            │  GET  /openc3-api/stream?topic=  StreamingApi (NDJSON)       │
            └───────────────┬──────────────────────────────────────────────┘
                            │  SCOPE__DECOM__{TARGET}__PACKET
                            ▼
   definitions/droid_sim/          ┌─────────────────────────────┐
   cmd_tlm/*.txt  ────────────────▶│  cosmos_tlm_bridge.models    │
   (the same files COSMOS loads)   │  POLY conversions + LIMITS   │
                                   └──────────────┬──────────────┘
                                                  ▼
                                ┌──────────────────────────────────┐
                                │  TelemetryFrame (typed)          │
                                │  point.dn / point.eu             │
                                │  point.limits_state / limits_color│
                                └───┬────────────┬─────────────┬───┘
                                    │            │             │
                        Pydantic ───┘     Go structs      webhook JSON
                        (Python svc)      (tlm-bridge)     (React/any)
```

### Components

| Path | What it is |
| --- | --- |
| `src/cosmos_tlm_bridge/models.py` | OpenC3 `cmd_tlm/*.txt` parser, `POLY_READ_CONVERSION`, `LIMITS`, limits-state machine |
| `src/cosmos_tlm_bridge/telemetry.py` | `TelemetryPoint` / `TelemetryFrame` Pydantic models + roll-up |
| `src/cosmos_tlm_bridge/client.py` | Zero-dependency JSON-RPC client (stdlib `urllib`), keyless/core/enterprise auth |
| `src/cosmos_tlm_bridge/bridge.py` | Fan-out to microservice webhooks, per-point subscriptions |
| `src/cosmos_tlm_bridge/regress/` | YAML suite schema, runner, JUnit + HTML reporters |
| `src/cosmos_tlm_bridge/mock/` | `DROID_SIM` plant simulator + OpenC3-compatible HTTP server |
| `definitions/droid_sim/` | Real OpenC3 COSMOS v5 cmd/tlm definitions |
| `go/` | Typed Go client, limits engine, bridge, `tlm-bridge` CLI |
| `docker/sink.py` | Downstream microservice stand-in |

---

## The `DROID_SIM` target

Three packets, 27 typed points, real OpenC3 definition syntax:

| Packet | Points | Stream topic |
| --- | --- | --- |
| `ADCS` | `ATTITUDE_ERROR`, `SLEW_ACTIVE`, `SLEW_TARGET_ID`, `RW_SPEED` + CCSDS header | `DEFAULT__DECOM__{DROID_SIM}__ADCS` |
| `EPS` | `BATT_VOLTAGE`, `BATT_CURRENT`, `BATT_SOC`, `BATT_TEMP` + CCSDS header | `DEFAULT__DECOM__{DROID_SIM}__EPS` |
| `THERMAL` | `RADIATOR_TEMP`, `RW_TEMP`, `BUS_TEMP`, `HEATER_DUTY` + CCSDS header | `DEFAULT__DECOM__{DROID_SIM}__THERMAL` |

Channels use the ingenium-lib-cosmos convention `TARGET__PACKET__TELEMETRY_POINT`
(exactly two `__` delimiters), e.g. `DROID_SIM__THERMAL__RW_TEMP`.
`PACKET__POINT` shorthand is accepted when a default target is configured.

```
$ cosmos-tlm-bridge limits
DROID_SIM THERMAL
  point           units   DN range          EU range          blue (op)   conversion
  RADIATOR_TEMP   C       500..10000        -35..60           [-15,35]    POLY -40+0.01x
  RW_TEMP         C       2000..11000       -20..70           [10,45]     POLY -40+0.01x
  BUS_TEMP        C       1000..9500        -30..55           [-5,30]     POLY -40+0.01x
```

### Commands

| Command | Effect on the simulation |
| --- | --- |
| `CMD_SLEW_TARGET SLEW_RATE TARGET_ID DURATION ABORT_ON_LIMITS` | Starts a maneuver. Wheel heating is `24 + 14×SLEW_RATE °C`; ≥ 2.2 dps crosses `YELLOW_HIGH` (55 °C), 10 dps reaches `RED_HIGH` (70 °C). |
| `CMD_SET_LIMITS LIMITS_SET` | Switches the active COSMOS limits set (`DEFAULT`, `TVAC`, `SAFE`). |

The plant is deterministic (seeded) so CI is reproducible. An interface thread
writes packets into the current value table at 4 Hz, exactly as a COSMOS interface
would, so `get_tlm_cnt` advances whether or not anyone is reading.

---

## DN / EU semantics

| Turion vocabulary | COSMOS | JSON-RPC | ingenium suffix |
| --- | --- | --- | --- |
| `DN` | `RAW` | `tlm_raw` | `__RAW` |
| `EU` | `CONVERTED` | `tlm` | `__CONVERTED` |
| - | `FORMATTED` | `tlm_formatted` | - |

Conversions come from the definition, e.g. `POLY_READ_CONVERSION -40.0 0.01`:
`EU = -40 + 0.01 × DN`. The bridge keeps limits in the EU domain (as COSMOS does)
and *re-derives* them into the DN domain by inverting the polynomial, so a DN-mode
assertion compares against the identical physical threshold:

```
RW_TEMP  yellow_high = 55.0 °C (EU)  ⟷  9500 counts (DN)
```

That equivalence is covered by both the Python and Go test suites.

## Limits states

Full COSMOS `LIMITS` semantics, including persistence and the optional BLUE
operational band:

| Condition | State | Colour |
| --- | --- | --- |
| `value ≤ RED_LOW` | `RED_LOW` | RED |
| `RED_LOW < value ≤ YELLOW_LOW` | `YELLOW_LOW` | YELLOW |
| `YELLOW_HIGH ≤ value < RED_HIGH` | `YELLOW_HIGH` | YELLOW |
| `value ≥ RED_HIGH` | `RED_HIGH` | RED |
| `GREEN_LOW ≤ value ≤ GREEN_HIGH` | `BLUE` | BLUE (operational) |
| otherwise | `GREEN` | GREEN |

`limits_overall` is the worst colour including BLUE; `cosmos_overall_limits_state`
collapses BLUEtoGREEN to match `get_overall_limits_state`, which only returns
GREEN/YELLOW/RED.

---

## Declarative suites

```yaml
suite: droid_pass_acceptance
target: DROID_SIM
scope: DEFAULT
timeout_s: 105
fresh_sim: true            # reset the plant first (no-op on a real COSMOS)

steps:
  - name: command the attitude slew
    cmd: DROID_SIM CMD_SLEW_TARGET with SLEW_RATE 2.5, TARGET_ID 7, DURATION 60
    check: ENABLED

  - name: slew is acknowledged in telemetry (DN mode)
    assert:
      - channel: ADCS__SLEW_ACTIVE
        dn_eu: DN
        condition: "EQUAL,1,,,"
        within_s: 25

  - name: reaction wheel thermal limit violation is caught (EU mode)
    assert:
      - channel: THERMAL__RW_TEMP
        dn_eu: EU
        expect_limits: YELLOW_HIGH      # never hardcodes 55.0
        within_s: 45
```

### Step types

| Step | Keys |
| --- | --- |
| Command | `cmd`, `check` (`ENABLED`/`NO_CHECK`), `settle_s` |
| Assert | `assert:` list of assertions, `require_all` |
| Sleep | `wait_s`, `reason` |
| Wait for packet | `wait_for_packet`, `within_s` |
| Wait for limits | `wait_for_limits`, `packet`, `within_s` |

### Assertion fields

| Field | Values |
| --- | --- |
| `channel` | `TARGET__PACKET__POINT` or `PACKET__POINT` |
| `dn_eu` | `DN` (aliases `RAW`) or `EU` (alias `CONVERTED`) |
| `condition` | `EQUAL,a,,,` and `BETWEEN,a,b,,` and `OUTSIDE,a,b,,` and `CHANGE,delta,,` and `TOLERANCE,a,tol,,` and or operator form `">= 55.0"`, `"< 45"`, `"== 7"`, `"1..3"` |
| `expect_limits` | `GREEN`, `YELLOW_LOW`, `YELLOW_HIGH`, `BLUE`, `RED_LOW`, `RED_HIGH` (aliases `YELLOW`, `RED`, `OPERATIONAL`) |
| `expect_limits_color` | `GREEN`, `YELLOW`, `RED`, `BLUE` |
| `within_s` | sampling window |
| `occurrence` | `ANY` (default), `ALWAYS` |
| `sample_interval_s` | poll cadence, default 0.15 |

The `verification_condition` CSV shape matches ingenium's
`ing_lib.steps.verify_wait_telemetry` contract, so suites can be ported to and from
the Ingenium custom-script format.

### Bundled suites

| Suite | Proves | Runtime |
| --- | --- | --- |
| `droid_smoke.yaml` | JSON-RPC, DN/EU conversion, typing | ~0.2 s |
| `droid_thermal_red.yaml` | RED_HIGH caught in DN + EU, system roll-up | ~3.5 s |
| `droid_pass_acceptance.yaml` | Full pass: command to DN/EU assertions to `YELLOW_HIGH` catch to recovery | ~23 s |

---

## CLI

```
cosmos-tlm-bridge demo      [--suite NAME] [--out DIR] [--budget 45]
cosmos-regress   run  SUITE [--out DIR]
cosmos-tlm-bridge serve     [--host 0.0.0.0] [--port 2900] [--auth-required]
cosmos-tlm-bridge bridge    [--packet EPS THERMAL] [--webhook URL] [--duration 30]
cosmos-tlm-bridge verify    CHANNEL... [--limits] [--json]
cosmos-tlm-bridge limits
cosmos-tlm-bridge targets
```

### Reports

`cosmos-regress` writes three artifacts per suite:

* `*-junit.xml`. GitHub Actions / GitLab / Jenkins;
* `*-report.html`. self-contained (inline CSS/SVG, no CDN) for air-gapped archiving;
* `*-results.json`. every sample behind every assertion.

---

## Webhook payload contract

The same shape from Python and Go, so a React or Go service needs one decoder:

```json
{
  "schema_version": "1.0",
  "scope": "DEFAULT",
  "target": "DROID_SIM",
  "packet": "THERMAL",
  "topic": "DEFAULT__DECOM__{DROID_SIM}__THERMAL",
  "packet_time": "2026-11-03T07:16:04Z",
  "received_count": 26,
  "limits_overall": "YELLOW",
  "cosmos_overall_limits_state": "YELLOW",
  "points": {
    "RW_TEMP": {
      "channel": "DROID_SIM__THERMAL__RW_TEMP",
      "dn": 9560,
      "eu": 55.6,
      "units": "C",
      "formatted": "55.60 C",
      "limits_state": "YELLOW_HIGH",
      "limits_color": "YELLOW"
    }
  }
}
```

### Go

```go
client := cosmostlm.NewClient(cosmostlm.Config{
    BaseURL: "http://cosmos:2900", Scope: "DEFAULT", AuthMode: "keyless",
})
bridge := cosmostlm.NewBridge(&cosmostlm.WebhookSink{URL: "http://svc:8080/tlm"})
bridge.OnAlarm = func(f cosmostlm.TelemetryFrame, s cosmostlm.LimitsSummary) {
    log.Printf("LIMITS %v", s.Violations)
}
bridge.Run(ctx, client, []string{"ADCS", "EPS", "THERMAL"}, 4 /* Hz */, 0)
```

```bash
cd go
go run ./cmd/tlm-bridge -cosmos http://localhost:2900 -packets ADCS,EPS,THERMAL \
                        -webhook http://localhost:8080/tlm -duration 30s
go run ./cmd/tlm-bridge -verify 'DROID_SIM__THERMAL__RW_TEMP,DN'
```

### Python

```python
from cosmos_tlm_bridge import CosmosClient, TelemetryBridge, load_target, LimitsState

client = CosmosClient(base_url="http://cosmos:2900", auth_mode="keyless")
target = load_target("definitions/droid_sim", "DROID_SIM")
bridge = TelemetryBridge(client, target, webhooks=["http://svc:8080/tlm"])

def alarm(frame, summary):
    print(frame.packet, summary["items"])

bridge.on_violation(alarm)
bridge.run(["ADCS", "EPS", "THERMAL"], duration=30)
```

---

## Verification

```bash
make test          # 80 Python tests + Go tests
make demo          # acceptance run, self-checking spin-up budget
```

What the test suite pins down:

* Definition parsing. `LIMITS` argument order, `POLY_READ_CONVERSION`, units, `STATE`.
* Conversion round-trips. forward, inverse, and quadratic inverse polynomials.
* Limits precedence. all 13 boundary cases for `RW_TEMP`, plus the rule that a
 wide BLUE band can never mask a YELLOW or RED violation.
* DN ≡ EU thresholds. `yellow_high = 55 °C` evaluates to `YELLOW_HIGH` at
 DN 9500 in both domains, and `GREEN` at 9499.
* No bridge/COSMOS divergence. every emitted frame's limits state is
 cross-checked against COSMOS' own `get_tlm_packet` evaluation.
* Negative controls. a suite expecting `RED_HIGH` where only `YELLOW_HIGH`
 occurs must FAIL, and so must a wrong value range. This is what stops the
 assertions from being decorative.
* Reports. JUnit `<failure>` is emitted exactly when a case fails.
* Transport. both JSON-RPC parameter forms, both API paths, core-auth round
 trip, NDJSON streaming, and webhook retry/failure counting.

---

## Definition of done to where it is proven

| Criterion | Evidence |
| --- | --- |
| `uvx cosmos-tlm-bridge demo` / `docker compose up` initialises a DROID pass in < 45 s | `cosmos-tlm-bridge demo` prints `instant spin-up 22.7s / 45.0s PASS` and exits non-zero if it exceeds it; the CI `acceptance` job asserts the same budget |
| `CMD_SLEW_TARGET` triggers automated assertions in both DN and EU | `droid_pass_acceptance.yaml` steps *slew is acknowledged in telemetry (DN mode)* + *slewing target slot decodes… (EU mode)* |
| Correctly catches a simulated `YELLOW_HIGH` reaction-wheel thermal violation | *reaction wheel thermal limit violation is caught (EU mode)* and *(DN mode)*, plus a RED_HIGH suite; negative controls in `tests/` prove they fail when the expectation is wrong |
| Telemetry streams into typed Go/Python structs and forwards to downstream webhooks, no Ruby in OpenC3 | `go run ./cmd/tlm-bridge -webhook …` to 24 frames / 24 webhook_ok / 0 failed; `TelemetryBridge` + `docker/sink.py`; no `.rb` file exists in this repository |
| Full CI acceptance in < 105 s | suite sweep (`smoke` + `thermal_red` + `pass_acceptance`) ≈ 27 s; CI job fails above 105 s |

---

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `COSMOS_URL` | `http://localhost:2900` | COSMOS base URL |
| `COSMOS_SCOPE` | `DEFAULT` | COSMOS scope |
| `COSMOS_AUTH_MODE` | `keyless` | `keyless`, `core`, `enterprise` |
| `COSMOS_PASSWORD` | - | required for `core`/`enterprise` |
| `COSMOS_DEFINITIONS_DIR` | bundled | OpenC3 `cmd_tlm` directory |
| `COSMOS_PORT` | `2900` | reference-server port |
| `NO_COLOR` | - | disable ANSI colour |

---

## Design notes

* One source of truth for conversions. The bridge parses the same
 `cmd_tlm/*.txt` files COSMOS loads, so an item change cannot silently desync
 the test suite. There is no second definition to drift.
* The bridge verifies itself. Every polled frame compares its own limits
 evaluation against COSMOS' `get_tlm_packet` states and records any divergence.
 a mismatch surfaces as a suite error instead of a wrong green.
* Keyless first. `keyless` is the default auth mode so `demo` and CI need no
 credentials; `core` and `enterprise` follow the documented COSMOS endpoints.
* Small dependency surface. Pydantic and PyYAML. No OpenC3 SDK, no Ruby, no
 cloud SDK. deliberate parity with `turion-platform-drift`'s portability goals.

## License

Apache-2.0.
