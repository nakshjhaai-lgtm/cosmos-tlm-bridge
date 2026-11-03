# START HERE

This archive contains **two finished, independently working deliverables** built
for **Turion Space** ground software, plus the documentation needed to
understand, run, verify and extend them without any prior context.

If you are a human, read this page, then `HANDOVER.md`.
If you are an AI assistant starting a new chat: read this page, then
`HANDOVER.md`, then `DOCUMENTATION.md`. Everything you need is in these files —
nothing lives only in a previous conversation.

---

## What is in the box

| Folder | What it is | Language / stack | Status |
| --- | --- | --- | --- |
| `PROJECT-1-cosmos-tlm-bridge/` | **Bottleneck #1** — OpenC3 COSMOS v5 telemetry-to-microservice bridge + declarative CI regression runner. Replaces hand-maintained Script Runner (Ruby) procedures with YAML suites. | Python 3.11+ (Pydantic, PyYAML) + a parallel Go client/bridge, Docker assets | **Complete** — 80 pytest tests, 16 Go tests, 3 suites green, demo 22.8 s vs 45 s budget |
| `PROJECT-2-turion-platform-drift/` | **Bottleneck #2** — Go static analyzer that finds hard-coded AWS lock-in (boto3, AWS Batch, Lambda, IAM, S3 primitives, ECR, IRSA…) and **generates** the air-gapped Kubernetes/Crossplane replacement stack, then proves it applies. | Go 1.22+ (**zero external dependencies**), embedded Python AST helper | **Complete** — 99 tests, 84.3 % coverage, 26 rules, verified against a real kube-apiserver |

Supporting documents in this folder:

| File | Purpose |
| --- | --- |
| `HANDOVER.md` | **The briefing for a new chat session**: state, decisions, constraints, gotchas, next steps, glossary. |
| `DOCUMENTATION.md` | The technical manual: architecture, protocols, algorithms, rule catalog, CLI reference, data formats, for both projects. |
| `FILE-INVENTORY.md` | Every single file in the archive and why it exists. |
| `VERIFICATION-EVIDENCE.md` | The actual command outputs proving every Definition-of-Done clause. |
| `ENVIRONMENT.md` | Exact toolchain versions and how to rebuild the environment from nothing. |

Each project also has its own `README.md` (long-form, self-contained) and, for
project 2, `docs/verification.md` (the clause-by-clause audit).

---

## Prove it in five minutes

Nothing below needs credentials, a cloud account, or API keys.

### Project 1 — telemetry bridge + regression runner

```bash
cd PROJECT-1-cosmos-tlm-bridge
python3 -m venv .venv && .venv/bin/pip install -e ".[dev]"
.venv/bin/python -m pytest -q          # 80 passed
.venv/bin/cosmos-tlm-bridge demo       # 12/12 checks, ~23 s, budget 45 s
```

The `demo` command starts an in-process mock COSMOS, runs a full simulated LEO
pass (slew a reaction wheel, watch it go `YELLOW_HIGH`, roll the violation up to
a YELLOW system state), and writes JUnit + HTML + JSON reports.

### Project 2 — AWS lock-in analyzer + portable stack generator

```bash
cd PROJECT-2-turion-platform-drift
go test ./...                                             # 99 tests
go run turion-platform-drift scan ./ground-service        # scorecard: 0/100, grade F
go run turion-platform-drift scan ./ground-service \
    --generate --out ./reports --target kubernetes        # 27 portable objects
go run turion-platform-drift validate ./reports           # schema PASS, residual none
```

To reproduce the strongest gates (a real `kubectl apply --dry-run=client`, a
real apply into a bare cluster, and the Crossplane variant against upstream
CRDs), run:

```bash
scripts/verify.sh        # 32 gates, ~95 s, installs kubectl/helm/kwok itself
```

`scripts/verify.sh --no-cluster` runs the 23 gates that need no API server.

---

## The problem both projects solve

Turion Space flies small satellites and runs a ground segment that is currently
welded to two dependencies that make it expensive to operate and impossible to
deploy in a contested or air-gapped environment:

1. **Test procedures live in OpenC3 Script Runner scripts.** They are
   hand-maintained per satellite, per software build, in Ruby. Nothing about
   them is reviewable or diffable, and every new mission copies them.
   *Project 1 replaces them with declarative YAML suites and a typed bridge that
   turns COSMOS telemetry into normal service traffic.*
2. **The ground service stack assumes AWS.** boto3 calls AWS Batch, Lambda,
   S3 primitives and IAM; manifests reference ECR, IRSA, ALB and EBS. There is
   no path to running the same system on a ground station in a tent.
   *Project 2 finds every one of those couplings with real ASTs, scores the
   repository, and emits a replacement stack built only from Kubernetes'
   built-in API kinds — proven to apply to a bare cluster.*

---

## Ground rules that shaped everything (from the original brief)

- Build **exactly** as specified: exact names (`cosmos-tlm-bridge`,
  `turion-platform-drift`), exact commands (`uvx cosmos-tlm-bridge demo`,
  `cosmos-regress run droid_pass_acceptance.yaml`, `docker compose up`,
  `go run turion-platform-drift scan ./ground-service`), exact paths
  (`/__openc3_api__/api`), exact packet/channel names (`ADCS__ATTITUDE_ERROR`,
  `EPS__BATT_VOLTAGE`, `THERMAL__RADIATOR_TEMP`), command `CMD_SLEW_TARGET`.
- **Keyless** and zero-dependency framing for the Python package (Pydantic
  allowed). Project 2 has **zero** external Go dependencies.
- **No Ruby** may be required in OpenC3 for any acceptance flow.
- Performance budgets are contractual: the acceptance run must fit in 45 s and
  the scan in 30 s.

---

## Where the truth lives

- **Behaviour** is defined by tests, not prose. `go test ./...` and
  `pytest` are the specification.
- **Evidence** is in `VERIFICATION-EVIDENCE.md`; the raw artifacts are in
  `PROJECT-2-…/reports*/` (scorecards, SARIF, generated bundles) and
  `PROJECT-1-…/artifacts/` (JUnit/HTML/JSON per suite).
- **History:** project 2 is a git repository. Its reachable history is
  `36a3905` ("turion-platform-drift 1.0.0: AWS lock-in analyzer + portable stack
  generator", the complete tree) and `2dbbfdf` ("Harden verification harness
  against lost executable bits"). Earlier intermediate commits were lost when
  the build workspace was re-materialised from a snapshot, so treat `36a3905` as
  the baseline and read `git show 2dbbfdf` for the one follow-up change. Project 1
  was never a git repository — use `PROJECT-1-.../README.md` as its history.
