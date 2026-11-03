# turion-platform-drift

Cloud-agnostic ground-station portability analyzer for Turion Space.

`turion-platform-drift` is a static analyzer plus manifest generator. It reads a
ground-service repository, finds every hard-coded dependency on proprietary AWS
APIs, scores how far that code is from running air-gapped, and then writes the
portable replacement stack: Kubernetes Jobs, an S3-compatible object store,
RBAC, a NATS broker, PostgreSQL, an OpenTelemetry pipeline, and a Crossplane
composite that provisions all of it.

- Scanner. real ASTs for Go (`go/ast`) and Python (`ast`), a TypeScript
 source analyzer, and manifest analysis for YAML / JSON / HCL / Helm / shell.
 26 rules across 10 capability areas (compute, serverless, object-storage,
 identity, messaging, database, registry, network, config, observability).
- Generator. turns each finding into a portable object. AWS Batch job
 definitions become Kubernetes Jobs, ECR images are rewritten to the
 ground-station registry, and the object-store layer becomes a VersityGW or
 MinIO bucket that speaks the S3 wire protocol (so OpenC3 COSMOS targets and
 existing `boto3` code keep working with no AWS endpoint).
- Validator. three gates: an embedded schema check that needs nothing but
 the binary (so it runs on the ground station itself), a real
 `kubectl apply --dry-run=client` against a live API server when one is
 reachable (`--require-kubectl` makes it mandatory), and a residual-AWS scan
 that the generated bundles pass with zero matches. comments included.
- Zero external Go dependencies. Standard library only. Python analysis
 shells out to `python3` with an embedded analyzer (falls back to regex if
 Python is missing). Charts are rendered with `helm template` when helm exists.
- Proven, not asserted. `scripts/verify.sh` runs 32 gates, including a real
 kube-apiserver (kwok, no Docker), a real `kubectl apply` into a bare cluster,
 and a real apply of the Crossplane bundle against upstream CRDs. See
 `docs/verification.md`.

---

## Quick start

```bash
# The DoD command — full scorecard in well under a second
go run turion-platform-drift scan ./ground-service
```

```
  turion-platform-drift  ·  cloud-agnostic ground-station portability
  ─────────────────────────────────────────────────────────────────────────
  root      ./ground-service
  scanned   18 files  (8 yaml, 3 json, 3 python, 1 go, 1 hcl, 1 shell, 1 typescript)  in 0.05s
  analysis  go ast · python ast · yaml/json manifests

  PORTABILITY SCORE    0/100  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  F
  Cannot run off-AWS: 80 critical couplings block an air-gapped deployment.

  findings  112  critical   80  high   32  medium   0  low   0
```

Scan, generate the portable stack, and validate it in one pass:

```bash
go run turion-platform-drift scan ./ground-service --generate --out ./reports --target kubernetes
go run turion-platform-drift validate ./reports
```

Two targets, deliberately different:

- `--target kubernetes` (default) emits *only built-in API kinds*. no CRDs,
 no operators. It applies to a bare cluster in one pass, which is what makes
 "deploys air-gapped" true rather than aspirational.
- `--target crossplane` adds the operator-backed layer (Crossplane v2
 Composition, provider-minio `Bucket`, CloudNativePG `Cluster`, OTel collector,
 External Secrets) against the real, current API groups.

Verification, including a real API server:

```bash
make cluster     # etcd + kube-apiserver via kwok, no Docker required
make verify      # 32 gates: tests, race, fuzz, stress, real dry-run, real apply
```

```
  parsed     28 object(s) across 1 file(s)
  kinds      Bucket×3  Cluster×1  CompositeResourceDefinition×1  Composition×1
             ConfigMap×1  Deployment×2  ExternalSecret×1  Job×3  Kustomization×1
             Namespace×1  OpenTelemetryCollector×1  PersistentVolumeClaim×1
             Role×1  RoleBinding×1  Secret×2  SecretStore×1  Service×4
             ServiceAccount×1  StatefulSet×1
  schema     PASS  (0 error, 0 warning)
  residual   none — zero proprietary AWS references
  PASS
```

Build a binary once and drop the `go run`:

```bash
go build -o bin/turion-platform-drift . && ./bin/turion-platform-drift scan ./ground-service
```

---

## Commands

| Command | What it does |
| --- | --- |
| `scan <path>` | Analyze a repo and print the portability scorecard (text, JSON, or SARIF). |
| `generate <path>` | Scan, then write the portable replacement manifests. |
| `validate <dir>` | Validate manifests air-gapped: schema, required fields, residual AWS. |
| `rules` | List every rule, its severity, its portable replacement, and remediation. |
| `version` / `help` | Version and full flag reference. |

Flags may appear before or after the positional path.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--format text\|json\|sarif` | `text` | Scorecard output format. |
| `--out <dir>` | - | Write `scorecard.json`, `drift.sarif`, `scorecard.html` (and generated manifests) here. |
| `--generate` | off | Generate replacements during `scan`. |
| `--target kubernetes\|crossplane` | `kubernetes` | `crossplane` also emits an XRD + Composition. |
| `--namespace <ns>` | `ground-system` | Namespace for every generated object. |
| `--object-store versitygw\|minio` | `versitygw` | Which S3-compatible implementation to emit. |
| `--registry <host/path>` | `registry.ground.local` | Ground-station registry that replaces ECR. |
| `--split` | off | One file per object instead of a single bundle. |
| `--exclude <substr>` | - | Extra path exclusion (repeatable). |
| `--no-python` | off | Skip the Python AST pass, use the regex fallback. |
| `--top <n>` | `8` | Hotspots to print. |
| `--fail-under <score>` | - | Exit 1 when the score is below this value. |
| `--fail-on-critical` | off | Exit 1 on any critical coupling. |
| `--fail-on-any` | off | Exit 1 on any coupling at all. |
| `--fail-on-residual-aws` | off | Exit 1 if generated manifests still reference AWS. |
| `--kubectl <path>` / `--kubeconfig <path>` | `PATH` / env | Where the dry-run gate gets its binary and context. |
| `--require-kubectl` | off | Fail when the kubectl gate cannot run (CI uses this). |
| `--server-dry-run` | off | Also run `kubectl apply --dry-run=server` (real admission). |
| `--helm` / `--no-helm` / `--require-helm` | on / - / off | Render charts with `helm template` before validating. |

## Scoring

Findings are weighted by severity. critical 12, high 7, medium 3, low 1.
and folded through a saturating curve so the score cannot go negative:

```
score = round( 100 · exp(−penalty / 45) )      penalty = Σ severity weights
```

| Score | Grade | Meaning |
| --- | --- | --- |
| ≥ 90 | A | cloud-agnostic |
| ≥ 75 | B | portable with minor work |
| ≥ 55 | C | significant coupling |
| ≥ 30 | D | heavily AWS-bound |
| < 30 | F | cannot run off-AWS |

The `ground-service` fixture scores 0/100 (F). that is the intended result
for a fully AWS-bound service, and it is why the generator exists.

## Rule catalog

| Rule | Sev | Detects | Portable replacement |
| --- | --- | --- | --- |
| TPD001 | critical | `boto3` / `botocore` import | object-store shim + typed config |
| TPD002 | critical | boto3 client bound to a proprietary service | Kubernetes Job / Crossplane composite |
| TPD003 | critical | Lambda invocation (`invoke`, `InvocationType`) | Kubernetes Job + ClusterIP Service |
| TPD004 | critical | AWS Batch submission, job queue | Kubernetes Job manifest |
| TPD005 | high | hard-coded S3 bucket / ARN literal | MinIO / VersityGW bucket + ConfigMap |
| TPD006 | high | proprietary S3 primitive (`put_object`, presign…) | S3-compatible client factory |
| TPD007 | critical | `arn:aws` literal anywhere | Crossplane composite + ConfigMap |
| TPD008 | critical | IAM/STS role assumption, `AssumeRole` | ServiceAccount + workload identity |
| TPD009 | critical | non-portable IAM policy document | RBAC Role/RoleBinding + MinIO policy |
| TPD010 | high | SQS / SNS / Kinesis / EventBridge | NATS JetStream / Redpanda |
| TPD011 | high | DynamoDB / RDS / ElastiCache | CloudNativePG PostgreSQL |
| TPD012 | high | `aws …` CLI call in scripts | kubectl / Helm / Crossplane |
| TPD020 | critical | `aws-sdk-go(-v2)` import | S3-compatible client factory |
| TPD021 | critical | AWS SDK service client in Go | Kubernetes Job / object-store shim |
| TPD030 | critical | `@aws-sdk/*` import | S3-compatible client + HTTP |
| TPD031 | critical | AWS SDK Lambda client (TS) | Kubernetes Service |
| TPD040 | critical | Batch job definition manifest | Kubernetes Job |
| TPD041 | critical | Batch compute environment manifest | NodePool / PriorityClass |
| TPD042 | critical | ECR image reference | Harbor / Zot registry |
| TPD043 | critical | IRSA annotation `eks.amazonaws.com/role-arn` | ServiceAccount + Secret |
| TPD044 | high | AWS Load Balancer Controller ingress | NGINX Ingress / Gateway API |
| TPD045 | high | EBS/EFS storage class (`gp2`, `gp3`, `io1`…) | local-path / Longhorn / Ceph |
| TPD046 | critical | Secrets Manager / SSM CSI volume | Kubernetes Secret / External Secrets |
| TPD047 | high | Karpenter `EC2NodeClass` | NodePool + Cluster Autoscaler |
| TPD048 | high | CloudWatch Logs / X-Ray | OpenTelemetry Collector |
| TPD049 | critical | `aws_*` Terraform / CloudFormation resource | Crossplane composite |

`go run turion-platform-drift rules` prints the same catalog with full
remediation text; `rules --json` emits it machine-readable.

## What the generator emits

From `./ground-service`. Every object carries
`ground.turion.space/replaces` provenance labels, so an operator can trace a
manifest back to the coupling it removes.

`--target kubernetes`. built-in kinds only, applies to a bare cluster

| Portable object | Replaces |
| --- | --- |
| `Job` ×3 (`cronjob`, `droid-pass-processor`, `submit-pass-processing`) | AWS Batch job definition + queue (`vcpus`/`memory` carried over as cpu/memory requests) |
| `Deployment` ×3 + `Service` ×5 | Lambda functions and their invocations, object store, OTel, telemetry service |
| `StatefulSet` ×2 (`versitygw`, `nats`) + `PersistentVolumeClaim` ×2 | S3 buckets, SQS/SNS |
| `ConfigMap` ×3 (bucket inventory, OTel config, object-store endpoint) | S3 credentials/endpoint configuration, CloudWatch config |
| `Secret` ×4 + `ServiceAccount` + `Role` + `RoleBinding` | IAM roles, inline policies, IRSA, Secrets Manager |
| `Job` `objectstore-bucket-bootstrap` | S3 `CreateBucket` (signed with `curl --aws-sigv4`, no AWS SDK) |
| `Job`/`Deployment` postgres is a `StatefulSet` + `Service` + PVC | DynamoDB / RDS |
| `kustomize/kustomization.yaml` (overlay, not a cluster object) | Amazon ECR image rewriting |

`--target crossplane`. adds the operator layer

| Portable object | API group (verified against the live API server) |
| --- | --- |
| `Bucket` ×3 | `minio.crossplane.io/v1`. provider-minio v0.4.4, cluster-scoped |
| `Cluster` | `postgresql.cnpg.io/v1`. CloudNativePG |
| `OpenTelemetryCollector` | `opentelemetry.io/v1beta1` |
| `ExternalSecret`, `SecretStore` | `external-secrets.io/v1` (v1beta1 is no longer served) |
| `Composition`, `CompositeResourceDefinition` | `apiextensions.crossplane.io/v1`, v2 pipeline (`mode: Pipeline` + `functionRef`; `spec.resources` is rejected in Crossplane ≥ 2.0) |

Images are rewritten off ECR automatically
(`123456789012.dkr.ecr.us-west-2.amazonaws.com/turion/pass-processor:7` to
`registry.ground.local/turion/pass-processor:7`); the provenance annotation
records the repository path only, never the proprietary registry host.

## Validation, air-gapped

`validate` runs three independent gates:

1. Embedded schema. the repo's own YAML parser (`internal/yamlite`), a
 per-kind required-field table, cluster-scoped checks, `restartPolicy` and
 selector semantics. Needs nothing but the binary, so it works on the ground
 station it is describing.
2. `kubectl apply --dry-run=client`. the literal Definition-of-Done
 command. In a modern kubectl this validates against the *server's* OpenAPI, so
 it needs a reachable API server; `scripts/kwok-cluster.sh` provides one with no
 container runtime. `--server-dry-run` adds real admission, and
 `--require-kubectl` turns "kubectl is missing" into a failure instead of a
 note. When the cluster does not serve some `apiVersion`, that is reported per
 group version (`cluster does not serve: …`) rather than as a manifest defect.
3. Residual-AWS scan. `arn:aws`, `s3://`, `*.amazonaws.com`, IRSA
 annotations, ECR hosts and Batch manifest keys. The generated bundles contain
 zero AWS strings, comments included, so an auditor can grep them.

`--helm` (on by default in the CLI) renders charts with `helm template` and
validates the rendered manifests, so chart templates are checked rather than
skipped; `--require-helm` makes an unrenderable chart a hard error. A
`kustomization.yaml` is recognised as kustomize configuration (not a cluster
object), kept out of the applied bundle, and reported as `skipped`.

## Layout

```
main.go                     CLI: scan | generate | validate | rules | version
internal/rules/             26-rule catalog, severities, remediation text
internal/scan/              walker, Go AST, Python bridge, manifests, heuristics
  └ analyzer.py             embedded Python AST analyzer (JSON contract)
internal/pyast/             package copy of the analyzer (go:embed cannot use ../)
internal/score/             weighting, saturating curve, grades, gates
internal/generate/          portable object synthesis + Helm/Crossplane templates
internal/validate/          YAML parse, schema rules, residual-AWS detection
internal/yamlite/           dependency-free YAML subset parser and renderer
internal/report/            text / JSON / SARIF 2.1.0 / standalone HTML scorecards
ground-service/             the AWS-bound fixture the tool is proven against
templates/                  hand-written reference assets
  ├ crossplane/             provider config + GroundStack claim
  ├ opentelemetry/          collector config (OTLP → Prometheus/Loki/Tempo)
  └ helm/ground-service/    chart that renders the portable Job
testdata/clean-service/     decoy service with zero AWS coupling (0 findings)
testdata/edge-cases/        boto3 aliasing, dynamic services, chained calls
reports/                    generated bundle + scorecard.json/sarif/html
reports-cp/                 the same for --target crossplane
scripts/verify.sh           31-gate Definition-of-Done runner
scripts/kwok-cluster.sh     real kube-apiserver (etcd + apiserver, no Docker)
scripts/install-crds.sh     upstream CRDs at their current versions
docs/verification.md        clause-by-clause audit + bugs found
```

## Tests

```bash
make test        # go test ./...      — 99 tests, 84% statement coverage
make race        # go test -race ./...
make stress      # 3000-file repo, adversarial input, determinism, idempotency
make fuzz        # bounded fuzzing of every parser
make bench       # benchmarks
make verify      # the full Definition of Done, 32 gates
```

| Suite | What it proves |
| --- | --- |
| DoD / regression (`scan_test.go`) | fixture scan well under 30 s, all 26 rules fire, per-file detection, decoy service yields 0 findings |
| Accuracy matrix (`accuracy_test.go`) | one canonical probe per rule. a rule without a probe fails the build; a portable corpus that *talks* about S3/boto3/ARNs must stay silent |
| Brutal (`brutal_test.go`) | 3,000-file repository inside budget; CRLF/BOM/UTF-16/NUL/binary/1 MiB-line/500-deep-flow/400-deep-map inputs; unreadable files; symlink loops; 8 concurrent scans agree; scan is deterministic; generation is byte-for-byte idempotent; `--require-kubectl` cannot be faked off |
| Fuzz (`fuzz_test.go`, `internal/yamlite/fuzz_test.go`) | YAML parser, manifest scanner, Go AST scanner and the Python analyzer never panic on arbitrary bytes (1.5 M+ execs; found a real `strings.Repeat` crash) |
| `internal/generate` | kubernetes target emits only builtin kinds; crossplane target uses the current API groups and a v2 pipeline Composition; Batch job definitions mirrored with resources; ECR rewritten; kustomize overlay kept out of the bundle; split mode |
| `internal/score` | clean repo A/100, heavy coupling F, monotonic scoring, grade boundaries, gates, `Bar` clamping |
| `internal/validate` | required fields, `restartPolicy`, cluster-scoped namespace rejection, residual-AWS classes, Helm rendering (good and broken charts), kustomize overlay handling, CRD warnings. all hermetic, plus cluster-backed assertions that skip when no server answers |
| `internal/report` | SARIF 2.1.0 shape (driver, rules, results, regions), JSON document shape, empty-input null-safety, self-contained HTML, ANSI reset |
| `internal/yamlite` | nested maps/sequences, flow collections, comments, multi-document, quoting, block scalars, and errors on unterminated flow collections |
| `internal/scan` | file classification (including `Makefile`/`Dockerfile`/`.hcl`/`.jsonnet`), exclusions, collector dedupe and merge, read limits, policy-path detection, the no-Python fallback |
| CLI (`cli_test.go`) | every command, exit code, flag (including flags *after* the positional path), report files, gate flags (`--fail-under`, `--fail-on-*`, `--require-kubectl`), split/registry/object-store options |

## CI

`.github/workflows/ci.yml` runs four jobs:

1. build-test. Runs `gofmt -l`, `go vet`, build, `go test`, `go test -race`, and
   a guard that `go.mod` still has no `require` block.
2. accuracy. Runs the one-probe-per-rule matrix, the portable corpus, stress,
   adversarial input, determinism, and idempotency checks.
3. fuzz. Runs bounded fuzzing of the YAML parser, the manifest scanner, and the
   Go AST scanner.
4. portability-gate. Installs kubectl, helm, and kwokctl; starts a real
   kube-apiserver with no Docker; then runs the Definition of Done for real.
   It scans under 30 s, generates both targets, asserts that the air-gapped
   bundle contains only built-in kinds and zero AWS strings, runs `kubectl apply
   --dry-run=client` and `--dry-run=server`, installs upstream CRDs, validates
   the Crossplane bundle, applies the bundle to a bare cluster, fails on any
   warning, and runs validator gates with `--require-kubectl`.

## Verification record

`docs/verification.md` maps every clause of the specification to the gate and
command that proves it, and lists the eighteen bugs this verification found.
including three stale API groups, a Pod Security violation, a bundle ordering
race, a parser crash reachable from any malformed manifest, and five harness
defects (lost execute bits, a non-hermetic admission gate, helm's stderr
polluting the manifest stream, a missing helm prerequisite, and an XRD that only
looked valid because the object already existed).

## Design notes

- Zero false negatives over zero false positives. Every rule has a
 triggering fixture; the decoy service proves the false-positive floor is
 zero on portable code.
- The scanner is syntax-aware, not grep. Go uses `go/ast`, Python uses a
 real `ast` walk that collects imports, aliases, `from … import`, docstrings,
 bucket-literal bindings and chained calls. Prose in docstrings and
 commented-out code is not flagged.
- Findings are de-duplicated per rule per line so two scanners observing
 the same dependency do not inflate the penalty.
- Nothing is fetched at runtime. No network calls, no AWS SDK, no
 `kubectl` requirement. The tool is usable on the ground station it is
 describing.
- Every generated image is ground-local. `ghcr.io`, `docker.io`, `quay.io`
 and `public.ecr.aws` references are rewritten to `registry.ground.local`
 (or `--registry`). The validator and `scripts/verify.sh` fail if any public
 registry survives, so an air-gapped deployment never needs internet egress or
 a proprietary AWS API.
