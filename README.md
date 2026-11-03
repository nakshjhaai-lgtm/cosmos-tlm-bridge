`docker compose up`

![10-second DROID pass: OpenC3 JSON-RPC stream decommunicated into typed Go/Python structs, catching YELLOW_HIGH](docs/demo.gif)

[Terminal replay (.cast)](docs/demo.cast) · [Full transcript](docs/demo-replay.txt) · [Delivery verification](docs/DELIVERY-VERIFICATION.md) · [Definition of Done evidence](docs/DEFINITION-OF-DONE.md)

# cosmos-tlm-bridge

**OpenC3 COSMOS v5 → typed ground-microservice bridge + declarative CI regression runner, plus the `turion-platform-drift` ground-station portability linter.**

The one command above boots a keyless `DROID_SIM` OpenC3 v5 reference server, streams `ADCS__ATTITUDE_ERROR`, `EPS__BATT_VOLTAGE` and `THERMAL__RADIATOR_TEMP` through Pydantic/Go structs into a webhook sink, sends `CMD_SLEW_TARGET`, catches a simulated reaction-wheel `YELLOW_HIGH` limit violation in **both DN and EU**, writes JUnit + HTML reports, and exits — in under 45 seconds. No Ruby Script Runner procedure, no API key, no cloud SDK.

## Why this exists

Turion Space is scaling from single-satellite missions to multi-satellite **DROID** constellations, triggering an urgent hiring surge: a Staff Ground Software Engineer at **$225K**, a Sr. Ground SW Engineer, a Ground Systems SW Engineer, and a Space Operations Engineer. Operations and ground software engineers rely on **OpenC3 COSMOS v5** for command and control. Connecting COSMOS JSON-RPC telemetry streams (`TARGET__PACKET__TELEMETRY_POINT` channels) to downstream Go, Python and React ground microservices had become the bottleneck: every satellite software update or new mission concept required hand-written brittle Ruby/Python procedures in Script Runner, manually correlating raw Data Numbers (DN) with converted Engineering Units (EU) against `YELLOW_HIGH`/`RED_HIGH` limits. That made regression testing slow, error-prone, and painful for on-call.

`cosmos-tlm-bridge` replaces that loop with typed data, declarative YAML suites, and CI reports. `turion-platform-drift` removes the second bottleneck: ground-segment ETL coupled to proprietary AWS SDKs (EC2, S3, AWS Batch, Lambda, hardcoded ARNs) that cannot deploy cleanly to hybrid, air-gapped or on-premises ground stations.

## Exact commands

| Command | What it does |
| --- | --- |
| `docker compose up` | **The one-line demo.** Builds once, runs the full pass, writes reports, exits. |
| `uvx --from . cosmos-tlm-bridge demo` | Same self-contained demo from the local source tree. |
| `cosmos-regress run droid_pass_acceptance.yaml` | CI gate against a running COSMOS; emits JUnit + HTML + JSON. |
| `go run turion-platform-drift scan ./ground-service` | Full cloud-agnostic portability scorecard. |
| `bash scripts/verify-all.sh` | One-shot Definition-of-Done harness (auto-skips tools that are absent). |

> The literal `uvx cosmos-tlm-bridge demo` resolves from PyPI. This package is **not published to PyPI**, so that exact form currently returns “package not found”. The brief accepts either `uvx ...` **or** `docker compose up`; the latter works verbatim. `uvx --from . cosmos-tlm-bridge demo` is the local equivalent. See [delivery verification](docs/DELIVERY-VERIFICATION.md#6-residual-limitations).

## What is in the box

| Path | Deliverable |
| --- | --- |
| `src/cosmos_tlm_bridge/` | Pydantic models, DN→EU POLY conversion, limits engine, JSON-RPC client, keyless mock COSMOS, webhook bridge, YAML runner, JUnit/HTML reporters. |
| `go/` | Independent typed Go structs, JSON-RPC/StreamingApi client, limits parity, bridge and CLI. |
| `definitions/droid_sim/`, `suites/` | OpenC3 target definitions and three declarative acceptance suites. |
| `examples/react/TelemetryPanel.tsx` | Typed React consumer of the webhook frame. |
| `Dockerfile`, `docker-compose.yml`, `docker/` | Four-service finite demo: COSMOS, bridge, sink, regression runner. |
| `turion-platform-drift/` | Go + Python AST scanner, Kubernetes/Helm linter, Crossplane/OpenTelemetry templates, portable manifest generator. |
| `docs/` | Verification report, DoD evidence, 10-second GIF/replay, original delivery audit trail. |

## Definition of Done — measured

| Criterion | Result | Evidence |
| --- | --- | --- |
| `docker compose up` / demo initializes a LEO DROID pass in **< 45 s** | CI: plain `docker compose up` **36.976 s**, `demo` **23.595 s**; 30 s finite window | [`docs/evidence/ci-verification.log`](docs/evidence/ci-verification.log), [`demo.log`](docs/evidence/demo.log) |
| `CMD_SLEW_TARGET` triggers DN **and** EU assertions and catches `YELLOW_HIGH` on `DROID_SIM__THERMAL__RW_TEMP` | 15/15 checks; first sample DN 9536 / EU 55.36 °C; negative controls fail when expectations are wrong | [`pipeline.log`](docs/evidence/pipeline.log), [`droid_pass_acceptance-results.json`](docs/evidence/droid_pass_acceptance-results.json) |
| Packets stream into typed Go/Python structs and forward to webhooks; no Ruby; full CI acceptance **< 105 s** | 141 frames, 1269 points, 141 webhook deliveries, 0 failures, 0 `.rb` files; CI suite sweep **27.051 s** | [`verification-all.log`](docs/evidence/verification-all.log), [`suite.log`](docs/evidence/suite.log) |
| `go run turion-platform-drift scan ./ground-service` produces a scorecard in **< 30 s** | **0.220 s** in CI; 18 files / 112 findings / 26 rules | [`docs/evidence/ci-verification.log`](docs/evidence/ci-verification.log) |
| Flags 100% of hardcoded AWS dependencies (`boto3`, AWS Batch manifests) | 26/26 rule probes fire on the shipped AWS-bound fixture; portable decoy corpus stays silent | [`turion-platform-drift/docs/verification.md`](turion-platform-drift/docs/verification.md) |
| Generated Kubernetes/Crossplane manifests pass `kubectl apply --dry-run=client` and deploy without AWS APIs | exact client + server dry-runs and real apply all pass against kwok kube-apiserver; 0 residual AWS or public-registry references | [`turion-platform-drift/reports/`](turion-platform-drift/reports/), [`docs/evidence/ci-verification.log`](docs/evidence/ci-verification.log) |

Final GitHub Actions run `41670286164`: **23 passed, 0 failed, 0 skipped**; the linter’s full harness adds **33 passed, 0 failed**. Clause-by-clause detail: [`docs/DEFINITION-OF-DONE.md`](docs/DEFINITION-OF-DONE.md).

## Architecture

```
OpenC3 COSMOS v5 / DROID_SIM            Python Pydantic              Go structs
  POST /__openc3_api__/api       ─────►  TelemetryPoint  ─────────►  telemetry.Point
  POST /openc3-api/auth/verify           DN + EU + limits             DN + EU + limits
  GET  /openc3-api/stream                │                              │
  ADCS / EPS / THERMAL                   └──────────┬───────────────────┘
         │                                         ▼
         └───── cmd_tlm definitions ────► TelemetryFrame ──► webhook JSON ──► React / Go / Python
                                          │
                                          ▼
                          cosmos-regress YAML suites ──► JUnit + HTML
```

The mock implements both JSON-RPC paths plus `/openc3-api/auth/verify` and the StreamingApi NDJSON topic convention. It is an **OpenC3-compatible stand-in**, not the upstream COSMOS container: that keeps CI keyless and fast while preserving the documented protocol surface.

## Production readiness notes

- **Keyless / zero-SDK:** no OpenC3 SDK, AWS SDK, boto3, Ruby, API key or cloud credential. Python runtime deps are Pydantic + PyYAML only; the Go modules have no third-party `require` lines.
- **Air gap:** generated images are rewritten to `registry.ground.local` (or `--registry`); the validator and CI fail on `arn:aws`, `s3://`, `.amazonaws.com`, IRSA, ECR and public registries (`ghcr.io`, `docker.io`, `quay.io`, `public.ecr.aws`).
- **Honest limits:** the kwok-based cluster gates validate admission, scheduling and API semantics; they do not execute container images or reconcile Crossplane/CloudNativePG operators. “Zero false negatives” is proven on the shipped fixture corpus, not on arbitrary unseen source code. See [residual limitations](docs/DELIVERY-VERIFICATION.md#6-residual-limitations).

## GitHub repository setup

Target repository: **`nakshjhaai-lgtm/cosmos-tlm-bridge`**, public, with topics `openc3`, `cosmos`, `satellite-telemetry`, `ground-software`, `satellite-operations`.

The GitHub connection available in this sandbox can push to the session branch but cannot create repositories or set topics (`403 Resource not accessible by integration`). [`scripts/create-github-repo.sh`](scripts/create-github-repo.sh) is the prepared one-shot setup; reconnect GitHub in Arena with repository-creation permission, then run it. See [`docs/GITHUB-SETUP.md`](docs/GITHUB-SETUP.md).

## License

Apache-2.0.
