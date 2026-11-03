# FILE INVENTORY

Every file in this archive, and why it exists. 142 files (225 more inside
`PROJECT-2-turion-platform-drift/.git/`, which carries the repository history:
9 commits, `git status` clean).

Legend: **★** = read this first · **†** = evidence/artifact (generated, not
hand-written) · **⚠** = authored but never executed

---

## Archive root

| File | What it is |
| --- | --- |
| ★ `START-HERE.md` | One-page orientation: what is in the box, five-minute proof, ground rules. |
| ★ `HANDOVER.md` | The briefing for a new chat session: state, decisions, gotchas, non-claims, next steps, glossary. |
| ★ `DOCUMENTATION.md` | Technical manual for both projects (architecture, protocols, algorithms, rule catalog, CLI, formats). |
| `FILE-INVENTORY.md` | This file. |
| `VERIFICATION-EVIDENCE.md` | Every claim with the command that produced it. |
| `ENVIRONMENT.md` | Toolchain versions and how to rebuild the environment from nothing. |
| `evidence/verify-32-gates.log` † | Complete 32-gate Definition-of-Done run with timings. |
| `evidence/cluster-state.txt` † | apiserver v1.36.1, 45 CRD names, object/Bucket counts. |
| `evidence/crossplane-apply.log` † | Crossplane bundle applied to a clean cluster (27 created, 0 errors). |
| `evidence/scan-scorecard.txt` † | Full DoD scan output for `ground-service`. |
| `evidence/rules-catalog.txt` † | The 26-rule catalog as printed by the binary. |
| `evidence/project1-pytest.log` † | `80 passed`. |
| `evidence/project1-demo.log` † | Demo run, 22.8 s / 45 s. |
| `evidence/project1-suites.log` † | Three suites (3/7/15 checks) plus the limits table. |
| `evidence/project1-go-tests.log` † | 15 Go tests. |

---

## PROJECT-1-cosmos-tlm-bridge

Top level

| File | What it is |
| --- | --- |
| `README.md` | Long-form project documentation: problem, architecture, install, CLI, suite reference, reports, tests, DoD table. |
| `pyproject.toml` | Packaging: name `cosmos-tlm-bridge` 1.0.0, deps `pydantic` + `PyYAML`, console scripts `cosmos-tlm-bridge` and `cosmos-regress`, hatchling build, ruff/mypy/pytest config, bundles `definitions/` + `suites/` into the wheel. |
| `Makefile` | `install test test-py test-go lint fmt demo smoke red run serve bridge docker reports clean help`. |
| `Dockerfile` ⚠ | Multi-stage image: installs the wheel, entry point runs `cosmos-regress`. Never built (no Docker). |
| `docker-compose.yml` ⚠ | COSMOS + bridge + webhook sink stack. Never run. |
| `.github/workflows/ci.yml` | CI: install, pytest, Go tests, demo, suites, artifact upload. |
| `release/cosmos_tlm_bridge-1.0.0-py3-none-any.whl` † | Built wheel (installable artifact, verified by installing it). |

`src/cosmos_tlm_bridge/`

| File | What it is |
| --- | --- |
| `__init__.py` | Package exports and `__version__`. |
| `__main__.py` | `python -m cosmos_tlm_bridge` → CLI. |
| `models.py` | **The typed contract.** `TelemetryPoint`, `Limits`, `LimitsState`, `LimitsColor`, POLY conversion, DN↔EU, limits precedence (BLUE never masks YELLOW/RED), `is_violation`, severity rollup, instance caps. No I/O. |
| `telemetry.py` | Decommutation: target definition (COSMOS `cmd_tlm` text) → typed channels, `get_tlm_packet` tuples → `PointSample`s, channel-name parsing (`TARGET__PACKET__POINT`). |
| `client.py` | The only HTTP module: COSMOS JSON-RPC (`get_tlm_packet`, `get_tlm_values`, `get_limits`, `cmd`, `get_target_list`, …) at `/__openc3_api__/api`, scope handling, structured errors, streaming subscription helper. |
| `bridge.py` | Fan-out engine: `TelemetryBridge`, `PointSubscription`, `WebhookSink` (bounded retries, counters), `TelemetryFrame` with per-point DN/EU/limits and a frame summary. |
| `cli.py` | argparse front end: `demo run serve bridge verify limits targets version`; exit codes 0/1/2/3. |
| `mock/__init__.py` | Exposes the mock server. |
| `mock/droid.py` | The simulated DROID vehicle: battery charge/discharge, radiator thermal response, commanded slew that drives `RW_TEMP` into `YELLOW_HIGH` and back out. |
| `mock/server.py` | Keyless OpenC3-compatible HTTP server: JSON-RPC routes, packet/limits tables, command handling, fresh-sim reset. |
| `regress/__init__.py` | Exposes the runner. |
| `regress/schema.py` | Pydantic suite schema: `SuiteSpec`, `AssertionSpec` (channel, `dn_eu`, `condition`, `expect_limits`, `occurrence`), step models (`cmd`, `assert`, `wait_s`, `wait_for_packet`, `wait_for_limits`), `extra="forbid"`, `load_suite`. |
| `regress/runner.py` | Executes a suite: setup → steps → teardown (teardown always runs), per-assertion polling with deadlines, `ANY`/`ALWAYS`/`EVER` occurrence semantics, per-case results, suite timeout. |
| `regress/report.py` | Emits JUnit XML, self-contained HTML and JSON per suite. |

`go/` — independent Go client + bridge (module `github.com/turion-space/cosmos-tlm-bridge/go`, no third-party deps)

| File | What it is |
| --- | --- |
| `go.mod` | Module definition. |
| `types.go` | Telemetry types, limits precedence, POLY conversion, channel helpers, streaming topic/item naming. |
| `client.go` | JSON-RPC + StreamingApi client with scope and structured errors. |
| `bridge.go` | `TelemetryBridge`, `PointSubscription`, `WebhookSink`, `FrameSummary`. |
| `bridge_test.go` | Webhook delivery/retry, emit counts, frame summaries. |
| `limits_test.go` | Limits precedence, disabled limits, BLUE-vs-YELLOW/RED, POLY parity with Python, channel helpers, streaming keys. |
| `cmd/tlm-bridge/main.go` | Small runnable CLI over the library. |

`definitions/droid_sim/cmd_tlm/`

| File | What it is |
| --- | --- |
| `droid_sim_tlm.txt` | COSMOS text target definition: 3 packets (ADCS, EPS, THERMAL) × 9 typed points = 27 (incl. CCSDS headers), 12 named signals, 9 with limits sets incl. BLUE bands, POLY conversions. |
| `droid_sim_cmds.txt` | Command definitions: `CMD_SLEW_TARGET` (slew rate, target id, duration, abort-on-limits) and `CMD_SET_LIMITS` (limits set), each with CCSDS header items. |

`suites/`

| File | What it is |
| --- | --- |
| `droid_smoke.yaml` | 3-check fast smoke suite (packets streaming, bus voltage in band). |
| `droid_thermal_red.yaml` | 7-check RED_HIGH thermal suite (an independent stress of the limits engine). |
| `droid_pass_acceptance.yaml` | 15-check full LEO pass: slew, acknowledge, EPS band, `YELLOW_HIGH` in both DN and EU, radiator healthy, recovery, teardown. |

`tests/`

| File | What it is |
| --- | --- |
| `test_models_and_limits.py` | Models, conversions, limits precedence, BLUE/GREEN semantics, severity rollup, instance caps. |
| `test_bridge_and_regress.py` | Client, mock server, bridge subscriptions/webhooks, suite loading, positive run, **negative control** (wrong expectation must fail), timeout and teardown behaviour. |

`docker/`

| File | What it is |
| --- | --- |
| `entrypoint.sh` ⚠ | Waits for COSMOS, then runs the requested suite. |
| `sink.py` ⚠ | Webhook sink for the compose demo: prints received typed frames. |

`artifacts/` † — reports from real runs

| File | What it is |
| --- | --- |
| `droid_smoke-{junit.xml,report.html,results.json}` | Smoke suite results. |
| `droid_thermal_red-{junit.xml,report.html,results.json}` | RED_HIGH suite results. |
| `droid_pass_acceptance-{junit.xml,report.html,results.json}` | Acceptance suite results (15/15). |
| `droid_pass-{junit.xml,report.html,results.json}` | Earlier acceptance run kept as a comparison artifact. |

---

## PROJECT-2-turion-platform-drift

Top level

| File | What it is |
| --- | --- |
| `README.md` | Long-form documentation: problem, install, commands, rule table, scoring, generation, validation, tests, CI, verification record, layout. |
| `go.mod` | `module turion-platform-drift`, Go 1.22, **no `require` block** (zero external dependencies — a tested property). |
| `.gitignore` | Ignores build output and scratch dirs. |
| `Makefile` | `build test race stress fuzz bench cover scan generate validate cluster crds verify verify-fast ci demo clean help`. |
| `main.go` | CLI: `scan generate validate rules version help`, flag parsing with `reorderArgs`/`boolFlags` (flags may follow the positional path), exit codes, gate flags (`--fail-under`, `--fail-on-critical`, `--fail-on-any`, `--fail-on-residual-aws`). |
| `docs/verification.md` | Clause-by-clause DoD audit, the 18-bug table, Crossplane API-version table, and the explicit "not claimed" list. |
| `.github/workflows/ci.yml` | Four jobs: build-test, accuracy, fuzz, portability-gate (kwok cluster, CRDs, both targets, real apply, dry-runs, SARIF upload, artifacts). |

Tests (Go)

| File | What it is |
| --- | --- |
| `scan_test.go` | End-to-end scan of the fixture: file counts, finding counts, rule coverage, report formats. |
| `accuracy_test.go` | One probe directory per rule (`probes/tpdNNN/…`, generated per test) so no rule can be masked; portable-corpus silence; precision checks. |
| `brutal_test.go` | Stress (3 000 files), adversarial tree (CRLF/BOM/NUL/binary/deep YAML), determinism, idempotency, encoding edge cases. |
| `fuzz_test.go` | Fuzz entry points for the scanner and generator. |
| `bench_test.go` | Four benchmarks (scan, manifest, yamlite, generate). |
| `cli_test.go` | ~25 CLI tests: every command, exit codes, flags after positional arguments, report files, gate flags. |

`internal/`

| File | What it is |
| --- | --- |
| `rules/rules.go` | The 26-rule catalog: id, severity, category, title, remediation, portable replacement; lookup helpers. |
| `scan/scanner.go` | File discovery, exclusion rules, language classification (`langFor` covers `.tf/.tfvars/.hcl/.nomad/.jsonnet/.libsonnet`), per-file dispatch, `ReadFile` with an 8 MiB ceiling, result aggregation. |
| `scan/golang.go` | Go AST analysis (`go/parser`): imports, selector call sites, string literals, annotations. |
| `scan/analyzer.py` | Embedded Python `ast` helper (runs once per scan over the file list, emits JSON; structured "unavailable" marker when Python is missing). |
| `scan/manifests.go` | YAML/JSON/HCL/Terraform/Helm/AWS-policy analysis: Batch job definitions and compute environments, ECR images, IRSA annotations, EBS/EFS classes, ALB, Secrets Manager CSI, Karpenter, CloudWatch; `IsAWSPolicyPath` (`.json` only, trust-aware). |
| `scan/findings.go` | Finding model, severity, symbol spans, same-line dedupe/merge. |
| `scan/scan_internal_test.go` | Unit tests for classification, exclusions, collector dedupe/merge, `ReadFile` limits, policy-path rules, no-Python fallback, broken Go source. |
| `pyast/analyzer.py` | Byte-identical copy of `scan/analyzer.py` — `go:embed` cannot reach a parent directory. **Keep the two files identical.** |
| `score/score.go` | Scoring: weights 12/7/3/1, penalty `100·(1−e^(−raw/45))`, grades A≥90/B≥75/C≥55/D≥30/F, verdicts, category and by-rule breakdowns, hotspot ordering. |
| `score/score_test.go` | Score monotonicity, grade bands, category ordering, hotspot stability. |
| `generate/generate.go` | Target-aware generation: naming, namespace, registry rewrite, bundle vs `--split`, deterministic bytes, kustomize overlay. |
| `generate/templates.go` | Every emitted object as a template: namespace, object store (versitygw/minio) shim, bucket bootstrap Job, OTel collector, identity/RBAC, PostgreSQL, NATS, Jobs, plus the Crossplane XRD/Composition/provider config, CNPG, ESO and OTel CRs. |
| `generate/generate_test.go` | Object counts, builtin-kinds-only for the kubernetes target, current API groups (minio/cnpg/eso/otel/crossplane), no stale groups, idempotency, determinism. |
| `validate/validate.go` | Validation: structural/schema checks, `kubectl apply --dry-run=client` and `=server`, residual-AWS scan, Helm rendering (stdout/stderr separated), honest `skipped (reason)` reporting, `--require-kubectl`/`--require-helm`. |
| `validate/validate_test.go` | Schema failures, skip-vs-require semantics, Helm render (good chart, broken chart, **stderr-noise regression test**), hermetic kubectl stubbing. |
| `report/report.go` | Renderers: text scorecard, JSON, SARIF 2.1.0, self-contained HTML. |
| `report/report_test.go` | SARIF structure/levels/fingerprints, JSON field stability, HTML escaping. |
| `yamlite/yamlite.go` | Tolerant YAML subset parser: multi-document, block scalars, comments, quoting, anchors rejected with a clear error; used for inspection only. |
| `yamlite/yamlite_test.go` | Parsing corpus, block scalars, documents, error paths. |
| `yamlite/fuzz_test.go` + `testdata/fuzz/FuzzParse/f1a026cd429be709` | Fuzz target and the **regression corpus entry** for the block-scalar panic. |

`scripts/` — the verification harness

| File | What it is |
| --- | --- |
| `verify.sh` | 32 gates in 7 sections; `--no-cluster` for the 23 that need no API server; prints PASS/FAIL + timing and a summary; exit 0 only if everything passes. Installs nothing itself but calls the scripts that do. |
| `kwok-cluster.sh` | `up|down|status` — installs `kwokctl` + `kubectl` if absent, starts etcd + kube-apiserver + controller-manager + scheduler (**no Docker, no kubelet**), prints a kubeconfig under `~/.kwok/clusters/<name>/kubeconfig.yaml`. |
| `install-crds.sh` | Installs the 45 CRDs the crossplane target needs, at current upstream versions (Crossplane 2.4.3, provider-minio 0.4.4, CNPG / ESO / OTel charts); installs `helm` itself when absent; prints served versions per kind. |

`ground-service/` — the AWS-bound fixture that is the scanner's subject

| File | What it is |
| --- | --- |
| `README.md` | What the fixture service does and every AWS coupling it contains. |
| `Makefile` | Fixture build/run helpers. |
| `go.mod` | Separate module so it does not join the parent module. |
| `etl/main.go` | Go ETL worker: `aws-sdk-go` S3/STS clients, `arn:aws` literals. |
| `storage/archive_tlm.py` | boto3 S3 archiver with hard-coded bucket/ARNs and proprietary primitives. |
| `lambda/invoke_telemetry_enrich.py` | Lambda handler + invocation of the enrichment function. |
| `batch/submit_pass_processing.py` | Submits to AWS Batch. |
| `batch/job-definition.json` | AWS Batch job definition (ECR image, role ARN). |
| `batch/compute-environment.json` | AWS Batch compute environment. |
| `batch/job-queue.yaml` | Batch job queue. |
| `iam/policy.json` | Non-portable IAM policy document. |
| `infra/main.tf` | `aws_*` Terraform resources. |
| `k8s/ground-service-deployment.yaml` | ECR image, IRSA annotation, Secrets Manager CSI volume, EBS storage class. |
| `k8s/karpenter-nodepool.yaml` | Karpenter node pool + EC2NodeClass. |
| `k8s/secrets-provider-class.yaml` | Secrets Store CSI `SecretProviderClass`. |
| `telemetry/otel-collector.yaml` | Collector configured with CloudWatch/X-Ray exporters. |
| `ui/src/api.ts` | `@aws-sdk/*` imports and Lambda client calls. |

`templates/` — reference material the validator renders and checks

| File | What it is |
| --- | --- |
| `crossplane/groundstack-claim.yaml` | Example namespaced `GroundStackClaim`. |
| `crossplane/provider-config.yaml` | Example `Provider`/`ProviderConfig` for the object store. |
| `opentelemetry/collector-ground.yaml` | Reference OTel collector CR. |
| `helm/ground-service/Chart.yaml` | Chart metadata (rendered by `helm template` in the harness). |
| `helm/ground-service/values.yaml` | Chart values. |
| `helm/ground-service/templates/pass-processor-job.yaml` | Templated pass-processor Job. |

`testdata/`

| File | What it is |
| --- | --- |
| `clean-service/main.go` | Portable Go service — must produce **zero** findings. |
| `clean-service/portable_archive.py` | Portable S3-API Python archiver — zero findings. |
| `clean-service/ui.ts` | Portable TypeScript client — zero findings. |
| `clean-service/deployment.yaml` | Portable manifest (no cloud annotations) — zero findings. |
| `edge-cases/dynamic_client.py` | Reflectively constructed boto3 client: the documented edge case where static analysis must not guess. |

`reports/` and `reports-cp/` † — committed evidence, regenerated by `scripts/verify.sh`

| File | What it is |
| --- | --- |
| `ground-station-portable.yaml` | The generated bundle: 27 objects (`reports/` = kubernetes target, `reports-cp/` = crossplane target). |
| `kustomize/kustomization.yaml` | Overlay so the bundle can be applied with `kubectl apply -k`. |
| `scorecard.json` | Machine-readable scorecard (stable field names for automation). |
| `scorecard.html` | Self-contained HTML scorecard (no external assets). |
| `drift.sarif` | SARIF 2.1.0 for GitHub code scanning. |

`.git/` (209 files) — repository history: `36a3905` (initial, full tree),
`2dbbfdf` (harness hardening), `8444fcc` (reproducible cluster gates),
`74d1ae2` (helm stderr), `fed91c3` + `c2bc370` (XRD v2 experiment and its
documented revert). See `HANDOVER.md` §2 note on lost intermediate commits.
