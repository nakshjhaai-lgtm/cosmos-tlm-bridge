# VERIFICATION EVIDENCE

Every claim in `START-HERE.md`, `HANDOVER.md` and `DOCUMENTATION.md` has a
command behind it. This file lists the commands, the results, and the raw logs
that were captured while producing this archive. Raw logs live in
[`evidence/`](#raw-logs-in-evidence) — read them if you want to see it rather
than take it on trust.

All runs below were made on **2026-11-03** against the exact contents of this
archive.

---

## 1. Project 1 — `cosmos-tlm-bridge`

| # | Claim | Command | Result | Log |
| --- | --- | --- | --- | --- |
| 1 | 80 Python tests pass | `.venv/bin/python -m pytest` | `80 passed in 51.51s` | `evidence/project1-pytest.log` |
| 2 | 16 Go tests pass | `cd go && go test ./...` | `ok github.com/turion-space/cosmos-tlm-bridge/go` — 16 `--- PASS` | `evidence/project1-go-tests.log` |
| 3 | Full acceptance pass inside the 45 s budget | `.venv/bin/cosmos-tlm-bridge demo` | 12/12 checks PASS, **instant spin-up 22.8 s / 45.0 s PASS** | `evidence/project1-demo.log` |
| 4 | Three declarative suites pass | `cosmos-regress run suites/*.yaml` | smoke **3/3**, thermal RED_HIGH **7/7**, pass acceptance **15/15**, 0 failed, 0 errors | `evidence/project1-suites.log` |
| 5 | Limits table is real (EU+DN, BLUE bands, POLY) | `cosmos-tlm-bridge limits` | 9 points across ADCS/EPS/THERMAL printed from the target definition | `evidence/project1-suites.log` (second half) |
| 6 | Negative controls actually fail | `tests/test_bridge_and_regress.py::test_negative_control_wrong_limits_state_fails` | passes *by asserting the wrong expectation fails* | part of run #1 |
| 7 | Wheel is shippable | `python -m build --wheel` | `release/cosmos_tlm_bridge-1.0.0-py3-none-any.whl` (60 KB) | file present in archive |

Reproduce:

```bash
cd PROJECT-1-cosmos-tlm-bridge
python3 -m venv .venv && .venv/bin/pip install -e ".[dev]"
.venv/bin/python -m pytest                 # 80 passed
.venv/bin/cosmos-tlm-bridge demo           # 22.8 s / 45 s
.venv/bin/cosmos-tlm-bridge serve &        # then, in another shell:
.venv/bin/cosmos-regress run suites/droid_pass_acceptance.yaml --out artifacts
```

**Not executed:** `docker compose up` / `make docker` (no container runtime was
available on the build machine). The Docker assets are authored and internally
consistent but unproven — see `HANDOVER.md` §3.

---

## 2. Project 2 — `turion-platform-drift`

### 2.1 The DoD command, verbatim

```
$ go run turion-platform-drift scan ./ground-service

  turion-platform-drift  ·  cloud-agnostic ground-station portability
  root      ./ground-service
  scanned   18 files  (8 yaml, 3 json, 3 python, 1 go, 1 hcl, 1 shell, 1 typescript)  in 0.04s
  analysis  go ast · python ast · yaml/json manifests

  PORTABILITY SCORE    0/100  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  F
  Cannot run off-AWS: 80 critical couplings block an air-gapped deployment.

  findings  112  critical   80  high   32  medium   0  low   0
```

Full output: `evidence/scan-scorecard.txt`. Every one of the **26 rules** fires
(`TPD001`…`TPD049`); the rule catalog printed by `turion-platform-drift rules`
is in `evidence/rules-catalog.txt`.

### 2.2 Tests, race detector, coverage

| # | Claim | Command | Result |
| --- | --- | --- | --- |
| 1 | Test suite | `go test ./...` | all packages `ok` |
| 2 | **99 tests** | `go test -v ./... \| grep -c '^--- PASS'` | `99` |
| 3 | Race-clean | `go test -race ./...` | `ok` |
| 4 | Coverage | `go test -coverpkg=./... -coverprofile=…` | **84.3 % of statements** |
| 5 | No external deps | `grep '^require' go.mod` | no match |
| 6 | Formatting / vet | `gofmt -l .`, `go vet ./...` | empty / clean |

### 2.3 The 32-gate Definition-of-Done runner

```
$ bash scripts/verify.sh
  … 32 gates …
  32 passed, 0 failed          # exit 0, 92 s wall clock
```

Full transcript: **`evidence/verify-32-gates.log`**. Gates, in order:

| Section | Gates |
| --- | --- |
| 1 hygiene | gofmt clean · `go vet` · `go build -trimpath` · zero `require` in `go.mod` |
| 2 correctness | `go test` · `go test -race` · every rule fires on its own probe · portable corpus silent |
| 3 stress/adversarial | 3 000-file repo ≤ 30 s · 15 hostile files (CRLF/BOM/NUL/binary/deep YAML) · deterministic scan · idempotent generation |
| 4 fuzzing | `yamlite` · manifest scanner · Go AST analyzer (each ~11–21 s) |
| 5 DoD scan | the DoD command · all 26 rules fire |
| 6 generation/validation | both targets · embedded schema validation of each · reference templates + Helm render · builtin-kinds-only grep · zero residual AWS identifiers |
| 7 cluster (kwok) | install 45 upstream CRDs · `--dry-run=client` (k8s + cp) · `--dry-run=server` (k8s + cp) · **real apply** k8s · **real apply** crossplane · `--require-kubectl --server-dry-run` |

**This run started from a machine with no cluster, no `kubectl`, no `helm`, no
`kwokctl` and no `~/.kube` or `~/.kwok`.** The scripts installed kubectl and
kwokctl themselves (`scripts/kwok-cluster.sh`) and helm
(`scripts/install-crds.sh`), brought up etcd + kube-apiserver, and ran every
gate. That is the reproducibility claim: *one command, no preconditions, no
credentials.*

### 2.4 Cluster facts after the run

From `evidence/cluster-state.txt`:

- `kube-apiserver` **v1.36.1**
- **45 CRDs** installed (Crossplane 2.4.3, provider-minio 0.4.4, CloudNativePG,
  External Secrets, OpenTelemetry Operator)
- **96 objects** in the cluster
- **3** `buckets.minio.crossplane.io` resources

### 2.5 Both generated stacks, and what really happens to them

| Target | Objects | Client dry-run | Server dry-run | Real apply |
| --- | --- | --- | --- | --- |
| `--target kubernetes` (built-in kinds only) | 27 | PASS | PASS | **27 created, 0 warnings, 0 errors** |
| `--target crossplane` (operator layer) | 27 | PASS | PASS | **27 created, 0 errors**, 1 Crossplane deprecation notice for XRD `v1` |

Crossplane transcript: `evidence/crossplane-apply.log`. The bundle-level checks:

```
$ grep -rE "arn:aws|\.amazonaws\.com|s3://|eks\.amazonaws\.com" \
      reports/ground-station-portable.yaml reports-cp/ground-station-portable.yaml
   (no output — zero AWS identifiers in either bundle)

$ grep -E "^kind: (Bucket|Cluster|OpenTelemetryCollector|ExternalSecret|SecretStore|Composition|CompositeResourceDefinition)$" \
      reports/ground-station-portable.yaml
   (no output — the kubernetes target contains only built-in kinds)
```

The word "AWS" does appear in both bundles, but only inside explanatory comments
and provenance annotations such as `ground.turion.space/replaces: aws-s3`; that
is documentation of what was replaced, not a dependency. The identifier grep
above is the contract.

### 2.6 Performance and stress numbers

| Measurement | Value |
| --- | --- |
| DoD scan of `ground-service` (18 files) | **0.04–0.05 s** (budget 30 s) |
| Stress: 3 000 files / 6 000 findings | ~0.55 s, 7 rules fired |
| Adversarial: 15 hostile files | 2 findings + 6 recoverable errors, no panic |
| `BenchmarkScanGroundService` | ~43 ms/op |
| `BenchmarkScanManifest` | ~102 µs |
| `BenchmarkYAMLiteParseBundle` | ~416 µs |
| `BenchmarkGenerate` | ~164 µs |
| Fuzzing (`yamlite`) | > 1.5 M executions, panic found and fixed |
| Fuzzing (manifest scanner) | > 166 k executions |
| Fuzzing (Go AST) | > 326 k executions |

---

## 3. Definitions of Done, clause by clause

### Project 1 — the observable contract

| # | Clause | Verdict | Evidence |
| --- | --- | --- | --- |
| 1 | Package named `cosmos-tlm-bridge`, installable, runnable with `uvx cosmos-tlm-bridge demo` | MET | `pyproject.toml`; `demo` runs from any install |
| 2 | `cosmos-regress run droid_pass_acceptance.yaml` runs a declarative suite | MET | run #4 above |
| 3 | Typed bridge for downstream microservices (value, units, limits state) | MET | `bridge.py`, `TelemetryFrame`, `--dry-run` output |
| 4 | No Ruby, no OpenC3 SDK, no AWS SDK, no API key | MET | deps are `pydantic` + `PyYAML` only |
| 5 | Full pass inside 45 s | MET | 22.8 s |
| 6 | Real limits detection (`YELLOW_HIGH`) in both DN and EU, rolled up to system state | MET | demo checks + `droid_thermal_red` suite |
| 7 | JUnit + HTML (+JSON) artifacts per suite | MET | `artifacts/` |
| 8 | Docker/compose assets | PARTIAL — authored, never executed | `HANDOVER.md` §3 |

### Project 2 — the observable contract

| # | Clause | Verdict | Evidence |
| --- | --- | --- | --- |
| 1 | `go run turion-platform-drift scan ./ground-service` produces a scorecard in < 30 s | MET | 0.04 s, score 0/100 grade F |
| 2 | Detects real AWS coupling across languages and manifests | MET | 26/26 rules, 112 findings (80 critical, 32 high) |
| 3 | `scan --generate` emits a portable replacement stack | MET | 27 objects per target |
| 4 | Emitted Kubernetes stack applies to a cluster without AWS | MET | real apply 27/0/0 |
| 5 | Crossplane variant valid against current upstream CRDs | MET | 45 CRDs installed; 27/0/0 |
| 6 | `validate` proves air-gapped correctness | MET | embedded schema + kubectl client/server dry-run + residual scan + Helm render |
| 7 | Zero false negatives on the fixture / zero false positives on portable code | MET | `TestOneProbePerRuleFires`, `TestPortableCorpusIsSilent` |
| 8 | Strain under stress and hostile input without crashing | MET | 3 000 files, adversarial tree, 3 fuzz targets |
| 9 | Zero external Go dependencies | MET | `go.mod` has no `require` |

Clause-by-clause audit with the full reasoning, including the "not claimed"
section, is in `PROJECT-2-turion-platform-drift/docs/verification.md`.

---

## 4. Bugs this verification pass found and fixed

18 defects were found by actually running things (the numbered table with
detection method is in `PROJECT-2-turion-platform-drift/docs/verification.md`).
The ones fixed while producing **this archive** — i.e. found by testing the
delivery itself on a clean machine — are:

| # | Bug | How it surfaced | Fix |
| --- | --- | --- | --- |
| 13 | Harness depended on the executable bit of `scripts/*.sh`; a copy that lost file modes aborted `verify.sh` with `Permission denied` | running the suite from an extracted copy | scripts invoked via `bash`, modes tracked as `100755` |
| 14 | `kubectl apply --dry-run=server` on the whole bundle failed on a **clean** cluster (`namespaces "ground-system" not found`) — server dry-run never persists the bundle's own Namespace | wiping the cluster and re-running | gate creates the namespace first, exactly as a real deployment does |
| 15 | Helm's **stderr** (kubeconfig permission warnings) was merged into the rendered manifest stream, creating a phantom first document (`missing apiVersion`) | a machine where a kubeconfig existed | stdout/stderr captured separately; regression test with a stub helm that writes to stderr |
| 16 | `scripts/install-crds.sh` hard-failed `missing helm` on a machine without helm, so the entire cluster section never ran | clean machine | the script installs helm itself, like the cluster script does for kubectl/kwokctl |
| 17 | The crossplane real-apply gate re-applied over existing objects, reporting `configured`/`unchanged` and never re-admitting a changed document | clean-cluster rerun | gate now requires all 27 objects to be created/configured/unchanged and rejects `invalid` as well as `error` |
| 18 | An experimental `CompositeResourceDefinition` at `apiextensions.crossplane.io/v2` looked "clean" because the object already existed; a fresh apply rejects it (`Claims aren't supported in … v2`) | deleting the XRD and re-applying | generator stays on XRD `v1` (documented deprecation notice); a `--dry-run=server` gate now always exercises admission |

Bugs 1–12 (from the earlier hardening pass) cover: kustomize overlay applied as
a cluster object, a non-existent `objectstorage.min.io` API group, a no-longer-
served `external-secrets.io/v1beta1`, Crossplane v2 rejecting `spec.resources`
on a Composition, pods violating the restricted Pod Security Standard, the
Namespace object racing the apply, the `yamlite` block-scalar panic, non-
reproducible bundle bytes, invalid `kubectl get --raw /apis -o json`, flags
ignored after the positional argument, non-hermetic tests, and double-counted
same-line findings.

---

## 5. Raw logs in `evidence/`

| File | What it proves |
| --- | --- |
| `verify-32-gates.log` | the complete 32-gate run, PASS/FAIL and per-gate timings |
| `cluster-state.txt` | apiserver version, 45 CRDs (all names), object and Bucket counts |
| `crossplane-apply.log` | crossplane bundle applied to a clean cluster: 27 created, 0 errors |
| `scan-scorecard.txt` | full `scan ./ground-service` output, 26 rules triggered |
| `rules-catalog.txt` | the 26-rule catalog as printed by the binary |
| `project1-pytest.log` | `80 passed` |
| `project1-demo.log` | 12/12 checks, 22.8 s of a 45 s budget |
| `project1-suites.log` | 3/3, 7/7, 15/15 plus the limits table |
| `project1-go-tests.log` | 16 Go tests |

---

## 6. What would change these numbers

- Running `scripts/verify.sh` again on a different machine, with a different
  Kubernetes minor version, or with Docker present may legitimately produce
  different output (e.g. newer CRDs, additional kubectl validation).
- The fuzz gates use a short time budget in `verify.sh` (11–21 s each) and a
  longer one in CI; execution counts will differ.
- The `ground-service/` fixture is fixed, so the score (0/100) is stable. Change
  the fixture and every count in §2 changes.
