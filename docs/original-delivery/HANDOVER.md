# HANDOVER — read this first if you are continuing the work

This document is the compressed state of the entire engagement. It is written so
that a new chat session (human or AI) can pick the work up cold: what exists,
why, what is proven, what is *not* proven, and what to do next.

Last updated: 2026-11-03. Both deliverables are complete and verified.

---

## 1. One-paragraph summary

Two independent deliverables were built for Turion Space's ground software.
**`cosmos-tlm-bridge`** is a Python (plus a parallel Go implementation) bridge
that decommutates OpenC3 COSMOS v5 telemetry into typed frames for downstream
microservices, ships a keyless mock COSMOS, and runs declarative YAML acceptance
suites that replace hand-maintained Ruby Script Runner procedures.
**`turion-platform-drift`** is a dependency-free Go CLI that scans a ground
service for hard-coded AWS lock-in using real Go/Python ASTs and manifest
analysis (26 rules), scores the repository, and **generates** the air-gapped
replacement stack — Kubernetes Jobs in place of AWS Batch, an S3-compatible
object store in place of S3, RBAC in place of IAM/IRSA, and optionally a
Crossplane composite — then validates and applies it against a real
kube-apiserver. Both are green under their full test suites; every clause of the
stated Definition of Done has an executed gate behind it.

---

## 2. What is DONE (do not redo)

### Project 1 — `cosmos-tlm-bridge`

| Item | Evidence |
| --- | --- |
| Python package with 8 CLI verbs (`demo`, `run`, `serve`, `bridge`, `verify`, `limits`, `targets`, `version`) | `pytest`: **80 passed** |
| Console scripts `cosmos-tlm-bridge` **and** `cosmos-regress` | `pyproject.toml` `[project.scripts]`, both → `cli:main` |
| Keyless mock COSMOS server implementing the JSON-RPC surface at `/__openc3_api__/api` | `src/cosmos_tlm_bridge/mock/` |
| DROID_SIM target definition: 3 packets, 10 telemetry points, `CMD_SLEW_TARGET` | `definitions/droid_sim/cmd_tlm/` |
| EU and DN limits domains, BLUE operational bands, POLY conversions | `.venv/bin/cosmos-tlm-bridge limits` |
| 3 declarative suites: smoke (3), thermal RED_HIGH (7), full pass acceptance (15) | all PASS, JUnit/HTML/JSON written to `artifacts/` |
| `demo` end-to-end acceptance run | **PASS 22.8 s / 45 s budget** |
| Parallel Go client + bridge (JSON-RPC + StreamingApi) | `go/`: **16 tests pass** |
| Wheel built | `release/cosmos_tlm_bridge-1.0.0-py3-none-any.whl` |
| Docker images + compose (COSMOS + bridge + webhook sink) authored | **never executed — no Docker in the build environment** |

### Project 2 — `turion-platform-drift`

| Item | Evidence |
| --- | --- |
| 26-rule catalog across 10 capability areas | `go run turion-platform-drift rules` |
| AST scanner: Go (`go/ast`), Python (`ast`, embedded helper), TypeScript, YAML/JSON/HCL/shell | 18 fixture files, **112 findings**, 26/26 rules fired |
| Scorecard: weighted severity → saturating curve → A–F, text/JSON/SARIF/HTML | `reports/scorecard.{json,html}`, `reports/drift.sarif` |
| Generator, `--target kubernetes`: **only built-in API kinds** (27 objects) | `reports/ground-station-portable.yaml` |
| Generator, `--target crossplane`: operator layer with current API groups | `reports-cp/ground-station-portable.yaml` |
| Validator: embedded schema + `kubectl apply --dry-run=client`/`=server` + residual-AWS scan + Helm rendering | `reports*` PASS, residual `none`, zero AWS identifiers in bundles |
| Real apply into a bare cluster: **27 objects, 0 warnings, 0 errors** | `scripts/verify.sh` gate |
| Crossplane bundle really applied against upstream CRDs | `scripts/verify.sh` gate |
| 99 tests, race-clean, **84.3 % statement coverage** | `go test ./...`, `-race`, `-coverpkg` |
| Fuzzing found and fixed a parser panic | `internal/yamlite/testdata/fuzz/FuzzParse/f1a026cd429be709` |
| 3,000-file repository scanned in **0.30 s** (budget 30 s) | `TestStressLargeRepository` |
| 32-gate DoD runner | `scripts/verify.sh` — **32 passed, 0 failed**, from a machine with no cluster and no kubectl/helm/kwok |
| Git history | `36a3905` (initial, full tree), `2dbbfdf` (harness hardening). Intermediate commits were lost to a workspace snapshot re-materialisation — see `START-HERE.md` |

---

## 3. What is NOT claimed (be honest about this)

1. **No Docker in the build environment.** Project 1's `Dockerfile`,
   `docker-compose.yml` and `docker/` assets are authored, reviewed and
   consistent with the code, but were **never built or run**. Do not claim
   `docker compose up` works — claim it is *ready to be run*. The same applies to
   `make docker`.
2. **kwok does not run containers.** In project 2's verification, pods are
   scheduled but not executed. Manifests, admission, scheduling and API
   semantics are real; image pulls, the VersityGW S3 handshake and PostgreSQL
   init are not exercised.
3. **Operators are not reconciling.** Crossplane / CloudNativePG / External
   Secrets / OpenTelemetry CRDs are installed in the verification cluster, so
   the custom resources are schema-validated and admitted, but no controller
   reconciles them. `--target kubernetes` exists precisely so a ground station
   has an option that needs none of them.
4. **The real OpenC3 COSMOS container was never run** (no Docker). Every COSMOS
   interaction is against the in-repo mock that implements the same documented
   routes; the Go and Python clients were written against the published OpenC3
   API docs (see §7).
5. **The AWS-side is a fixture, not a live account.** `ground-service/` is a
   realistic AWS-coupled service; nothing was deployed to AWS.

---

## 4. Decisions worth knowing before you change anything

**Project 1**

- *Both* console scripts point at the same `cli:main`; `cosmos-regress run X.yaml`
  and `cosmos-tlm-bridge run X.yaml` are the same command. The verb is `run` —
  there is no `regress` subcommand (a previous session wasted a minute on that).
- Suites never hard-code thresholds. They assert against the limits table read
  from the target definition, so a limits change does not silently break the
  suite — it changes what "YELLOW" means, exactly like the flight software.
- BLUE means "inside the optional operational band"; GREEN means "safe but
  outside the operational band". Only YELLOW/RED are violations. This mirrors
  COSMOS semantics and is asserted by `TestBlueBandNeverMasksYellowOrRed`.
- The mock server must stay **keyless**: no tokens, no `/auth/verify`
  round-trip required for local runs.
- `fresh_sim: true` in a suite restarts the simulated vehicle so the pass is
  reproducible; without it, runs inherit the previous state.

**Project 2**

- The Go module is named **`turion-platform-drift`**, not
  `github.com/turion-space/turion-platform-drift`. This is deliberate: the DoD
  command is literally `go run turion-platform-drift scan ./ground-service`, and
  a domain-qualified module path makes that bare package name unresolvable.
- **`--target kubernetes` is the default and emits only built-in API kinds.**
  A bundle that needs a CRD cannot deploy on a bare ground station, so the
  operator-free target is the honest default. `--target crossplane` is additive.
- The generated bundles contain **zero AWS identifiers** — no ARNs, no
  `*.amazonaws.com`, no `s3://`, no IRSA annotation — so an auditor can grep
  them. The word "AWS" itself *does* appear in explanatory comments and in
  provenance annotations like `replaces: aws-s3`; that is deliberate
  documentation of what was replaced. If you add a new object type, keep the
  identifier property (`--fail-on-residual-aws` enforces it).
- Same-rule findings on the same line are collapsed when their symbols overlap,
  so a whole-line match and a key match do not double-count the penalty.
- The validator never lies about the kubectl gate: if it cannot run it says
  `skipped (reason)`; `--require-kubectl` converts that into a failure. CI uses
  `--require-kubectl` everywhere.
- `scan`/`validate` accept flags **after** the positional path (custom
  `reorderArgs`), because Go's `flag` package stops at the first non-flag.

---

## 5. Environment gotchas (these cost real time last session)

| Gotcha | What to do |
| --- | --- |
| Go is **not** installed by default in the sandbox | `curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz \| tar -C /usr/local -xzf -` then `export PATH=/usr/local/go/bin:$PATH` |
| `uv` is often absent | use `python3 -m venv .venv && .venv/bin/pip install -e ".[dev]"` (works fine) |
| `kubectl --dry-run=client` needs a **server** in modern kubectl | `scripts/kwok-cluster.sh up` runs etcd + kube-apiserver with no Docker |
| kube-apiserver dies in sandboxes with **no default route** | pass `--extra-args kube-apiserver=advertise-address=127.0.0.1` (already in the script) |
| Go `flag` stops parsing at the first positional | `reorderArgs`/`boolFlags` in `main.go`; add new valueless flags to `boolFlags` |
| `go:embed` cannot reference `../` | `internal/scan/analyzer.py` and `internal/pyast/analyzer.py` are duplicates — keep them identical |
| Backticks inside Go raw strings terminate the string | avoid in templates; use `fmt.Sprintf` with `%[n]s` |
| `fmt.Sprintf` with no directives fails `go vet` | return a plain raw string |
| A sibling Go directory joins the parent module | `ground-service/` has its own `go.mod` on purpose |
| `dist/`, `build/`, `out/` are **excluded from workspace snapshots** | the wheel lives in `PROJECT-1-…/release/` for that reason |
| `kubectl get --raw /apis -o json` is invalid | discovery uses the raw endpoint without `-o` |
| `.git/config` is stripped from workspace snapshots | the archive ships a minimal one; if you ever re-clone/re-extract and `git status` complains, run `git config core.repositoryformatversion 0 && git config core.bare false` |
| Job pod templates are immutable | re-applying a bundle over a live Job fails; that is Kubernetes, not the tool |

---

## 6. How to continue (suggested next steps, in order of value)

1. **Build and run the Docker stack** (needs a Docker host): `make docker` in
   project 1, then `cosmos-regress run suites/droid_pass_acceptance.yaml`
   against the containerised COSMOS instead of the mock. That closes the largest
   remaining unknown.
2. **Run real operators** in the kubectl cluster (Crossplane + provider-minio,
   CNPG, ESO, OTel) and confirm the generated CRs reach `READY`. That converts
   "admitted" into "reconciled".
3. **Add a mission target**: copy `definitions/droid_sim/cmd_tlm/` to a new
   target directory and write a suite; nothing in the runner is DROID-specific.
4. **Extend the rule catalog**: add the rule to `internal/rules/rules.go`, a
   matching detector in `internal/scan/`, a probe in `accuracy_test.go`
   (`TestOneProbePerRuleFires` fails until you do) and a fixture file.
5. **Wire project 1 and 2 together**: have the bridge emit portability findings
   (e.g. a `/tlm` sink that refuses to start if `turion-platform-drift` finds
   critical couplings in the service it is about to talk to).

Things **not** to do: do not re-add a Ruby path to OpenC3, do not make the Python
package depend on an OpenC3 SDK or an AWS SDK, and do not add third-party Go
modules (the zero-dependency property is a tested requirement).

---

## 7. External references used (the research trail)

- OpenC3 JSON-RPC API — `POST /openc3-api/api`, `tlm`/`tlm_raw`/`tlm_formatted`/
  `tlm_with_units`, `keyword_params.scope`, default port 2900.
  <https://docs.openc3.com/docs/development/json-api>
- OpenC3 scripting API — `get_tlm_packet` returns
  `[[item, value, limits_state], …]`; `get_limits` returns
  `[set, persistence, enabled, red_low, yellow_low, yellow_high, red_high,
  green_low, green_high]`.
  <https://docs.openc3.com/docs/guides/scripting-api>
- COSMOS limits declaration syntax and BLUE/GREEN semantics.
  <https://ballaerospace.github.io/cosmos-website/docs/v5/telemetry>
- OpenC3 streaming API topic/item naming:
  `<MODE>__<CMD|TLM>__<TARGET>__<PACKET>`, items `…__<RAW|CONVERTED|FORMATTED>__`.
  <https://docs.openc3.com/docs/development/streaming-api>
- Ingenium COSMOS lib — channel naming `TARGET__PACKET__POINT`, DN↔`__RAW`,
  EU↔`__CONVERTED`, `POST /openc3-api/auth/verify`.
  <https://github.com/OpenIngenium/ingenium-lib-cosmos>
- OpenC3 Ruby API docs (telemetry scope keys). <https://rubydoc.info/gems/openc3>
- Kubernetes: `kubectl apply --dry-run` semantics, Pod Security Standards,
  Job immutability.
- Crossplane v2 CRD schemas (`apiextensions.crossplane.io_compositions.yaml`,
  `compositeresourcedefinitions`), provider-minio v0.4.4 CRDs, CloudNativePG
  chart CRDs, External Secrets chart CRDs, OpenTelemetry Operator chart CRDs —
  all fetched and applied in the verification cluster.
- kwok (`kubernetes-sigs/kwok`) binary runtime for a Docker-less API server.

---

## 8. Glossary

| Term | Meaning here |
| --- | --- |
| **COSMOS / OpenC3** | The open-source ground-station command & telemetry system Turion uses. |
| **DN / EU** | Digital Number (raw wire counts) / Engineering Units (converted, physical). |
| **BLUE / GREEN / YELLOW / RED** | COSMOS limits states: inside operational band / safe but outside band / low-tier violation / high-tier violation. |
| **Script Runner procedure** | The Ruby-based COSMOS test script this project replaces with YAML suites. |
| **Bottleneck #1 / #2** | The two problems in the brief: manual test procedures, and AWS lock-in. |
| **TPDnnn** | Rule IDs in project 2's catalog (`TPD001` = boto3 import, …). |
| **Lock-in / drift** | Code that cannot run without a proprietary cloud API; "drift" is the score of how far a repo is from portable. |
| **Crossplane** | Kubernetes-native control plane; used to provision the portable stack declaratively. |
| **XRD / Composition** | Crossplane's API definition (composite resource) and its implementation pipeline. |
| **kwok** | "Kubernetes WithOut Kubelet" — runs a real API server without a container runtime. |
| **SARIF** | Static-analysis results interchange format; uploaded to GitHub code scanning. |
| **DoD** | Definition of Done — the contractual acceptance clauses, itemised in `VERIFICATION-EVIDENCE.md` and `PROJECT-2-…/docs/verification.md`. |
