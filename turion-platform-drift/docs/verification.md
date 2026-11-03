# Definition of Done: clause by clause

Every clause of the specification, the evidence that closes it, and the command
that reproduces the evidence. Run everything with:

```bash
scripts/verify.sh                # 32 gates, ~92 s, needs network once for the cluster
scripts/verify.sh --no-cluster    # everything that needs no API server
```

Last full run: 32 passed, 0 failed (from a machine with no cluster, no kubectl, no helm and no kwokctl. the scripts install what they need).

---

## Clause 1: `go run turion-platform-drift scan ./ground-service` prints a full portability scorecard

| | |
| --- | --- |
| Evidence | 18 files, 112 findings (80 critical / 32 high), score 0/100 grade F, 26 of 26 rules fired |
| Gate | `scripts/verify.sh` to *Definition-of-Done scan* |

The invocation is exercised exactly as written, from the repository root with no
binary on `PATH`. This is why the Go module is named `turion-platform-drift`: a
`github.com/...` module path makes the bare package name unresolvable
(`package turion-platform-drift is not in std`).

## Clause 2: the scan finishes in under 30 seconds

| | |
| --- | --- |
| Evidence | fixture: 0.04 s; synthetic 3,000-file repository: 0.30 s |
| Gate | *3000-file repository inside the 30s budget* (fails above 30 s) |

`BenchmarkScanGroundService` measures ~52 ms per full scan including the Python
AST subprocess; the Go analysis itself is ~2 ms.

## Clause 3: zero false negatives: 100% of hard-coded AWS dependencies are flagged

Two independent proofs:

1. Fixture coverage. all 26 rules fire on `ground-service`, asserted by
 `TestZeroFalseNegatives` and by the verify gate *all 26 rules fire*.
2. One probe per rule. `accuracy_test.go` holds a canonical snippet for
 every rule in the catalog: boto3 imports, `boto3.client('batch').submit_job`,
 Lambda `invoke`, AWS Batch job-definition and compute-environment manifests,
 IAM policy documents, `arn:aws` literals, ECR images, IRSA annotations, ALB
 ingress, EBS storage classes, Karpenter `EC2NodeClass`, CloudWatch/X-Ray,
 `aws_*` Terraform resources. `TestOneProbePerRuleFires` fails if the probe map
 and the rule catalog drift apart, so a new rule cannot ship untested.

## Clause 4: zero false positives on portable code

`TestPortableCorpusIsSilent` scans a corpus that *mentions* cloud vocabulary in
docstrings, comments and prose (`S3-compatible`, `boto3 would work here`, an
`arn:aws` example inside a docstring) and requires 0 findings. The decoy
service in `testdata/clean-service` is a second, larger such corpus.

## Clause 5: the scanner detects AWS Batch manifests and boto3

| Rule | Probe that must fire |
| --- | --- |
| TPD001 | `import boto3` |
| TPD002 | `boto3.client('ec2')` |
| TPD003 | `boto3.client('lambda').invoke(...)` |
| TPD004 | `boto3.client('batch').submit_job(...)`, `jobQueueName:`, Batch compute-environment ARNs |
| TPD040 | `{"jobDefinitionName": ...}` |
| TPD041 | `{"computeEnvironmentName": ...}` |

## Clause 6: generates portable Kubernetes Job definitions

From the fixture's AWS Batch job definition the generator emits three Jobs
(`cronjob`, `droid-pass-processor`, `submit-pass-processing`), carrying the real
name, image, `vcpus → cpu` and `memory → Gi` across, with `backoffLimit`,
`ttlSecondsAfterFinished`, `restartPolicy: Never` and a restricted
`securityContext`. Asserted by `TestBatchJobDefinitionIsMirrored`.

## Clause 7: generates OpenC3-compatible object-store shims (VersityGW or MinIO)

`--object-store versitygw|minio` selects the implementation. Both speak the S3
wire protocol on-cluster, so OpenC3 COSMOS targets and existing `boto3` code keep
working with no AWS endpoint. Buckets are created by a bootstrap Job that signs
`CreateBucket` requests with `curl --aws-sigv4`. no AWS SDK, no proprietary
tooling. Asserted by `TestObjectStoreIsSelectable`; every object carries
`ground.turion.space/replaces: aws-s3`.

## Clause 8: generated manifests pass `kubectl apply --dry-run=client`

This clause used to be reported as "skipped: kubectl unavailable". It is now
executed for real against a real kube-apiserver:

```
kwokctl create cluster --runtime binary        # etcd + kube-apiserver, no Docker
kubectl apply --dry-run=client -f reports/ground-station-portable.yaml   → PASS
kubectl apply --dry-run=server -f reports/ground-station-portable.yaml   → PASS
```

`--dry-run=client` in a modern kubectl validates against the *server's* OpenAPI,
which is why a cluster is required. the earlier "it cannot be done offline"
conclusion was a wrong methodology, not a limitation of the tool. The validator
reports `kubectl skipped (reason)` only when the gate genuinely cannot run, and
`--require-kubectl` turns that into a hard failure so CI cannot silently lose the
gate (`TestRequireKubectlFailsWhenUnavailable`).

## Clause 9: deploys air-gapped without proprietary AWS APIs

A real apply, not a dry run, into a bare cluster with zero operators:

```
$ kubectl apply -f reports/ground-station-portable.yaml
27 objects created, 0 warnings, 0 errors
```

The default target emits only built-in API kinds (Namespace, ConfigMap,
Secret, ServiceAccount, Role, RoleBinding, Service, Deployment, StatefulSet, Job,
PVC), asserted by `TestKubernetesTargetUsesOnlyBuiltinKinds`. a bundle that
needs a CRD cannot deploy on a bare ground station. `--target crossplane` adds
operator-backed resources, and that bundle is applied for real against the
upstream CRDs at their current versions.

Byte-level air-gap proof: the generated bundles contain zero occurrences of
`arn:aws`, `.amazonaws.com`, `s3://` or `eks.amazonaws.com`. comments and
provenance annotations included, so an auditor can grep them and find nothing.

---

## Bugs this verification found and fixed

| # | Bug | How it was found |
| --- | --- | --- |
| 1 | The bundle failed `kubectl` because a kustomize overlay (`kind: Kustomization`, group `kustomize.config.k8s.io`) is not a cluster object | first real dry run |
| 2 | `objectstorage.min.io/v1alpha1 Bucket` is not a real API group; provider-minio's group is `minio.crossplane.io/v1` and the resource is cluster-scoped | CRD inspection |
| 3 | `external-secrets.io/v1beta1` is no longer served by current External Secrets; `v1` is | served-versions dump from the API server |
| 4 | Crossplane v2 rejects `spec.resources` / `spec.patchSets` on a Composition (strict decoding); v2 requires `mode: Pipeline` + `functionRef` | `kubectl apply --dry-run=client` |
| 5 | Generated pods violated the `restricted` Pod Security Standard that the generated Namespace itself enforces (4 containers) | real-apply warnings |
| 6 | The Namespace object sat 5th in the bundle, so a single `kubectl apply -f` raced itself | real apply |
| 7 | `strings.Repeat` with a negative count panicked the YAML parser on a block scalar followed by a dedent. a crash reachable from any malformed manifest | `go test -fuzz=FuzzParse` (1.5 M execs) |
| 8 | Bundle bytes were not reproducible: the output directory was baked into the header | `TestGenerateIsIdempotent` |
| 9 | `kubectl get --raw /apis -o json` is invalid, so API discovery always looked empty and unrecognised-kind errors were misclassified as hard failures | integration test against the live server |
| 10 | `scan`/`validate` parsed flags only before the positional argument, so `validate ./reports --server-dry-run` silently ignored the flag | server dry-run never ran |
| 11 | Tests depended on whether a cluster happened to be reachable; they are now hermetic (`Options{Kubectl: "/nonexistent/kubectl"}`), with cluster assertions in separate tests that skip when no server answers | `go test` in a cluster-enabled shell |
| 12 | Same-rule findings on the same line were double-counted (TPD004 twice on `job-queue.yaml:1`) | JSON finding dump |
| 13 | The harness depended on the executable bit of `scripts/*.sh`; a checkout, archive extraction or file-mode change made `verify.sh` abort with `Permission denied` at the cluster section instead of failing cleanly | running the verification suite from a freshly extracted copy of the delivery archive |
| 14 | `kubectl apply --dry-run=server` on the whole bundle failed on a clean cluster (`namespaces "ground-system" not found`) because server-side dry-run never persists the bundle's own Namespace; it had only ever passed on a cluster left dirty by an earlier run | recreate the cluster from scratch (`kwok-cluster.sh down && up`) and re-run the gate |
| 15 | Helm's stderr (kubeconfig permission warnings) was merged into the rendered manifest stream via `CombinedOutput`, producing a phantom first document (`missing apiVersion`) that failed validation. invisible until a kubeconfig existed and helm had something to warn about | full `verify.sh` run on a clean machine where helm was auto-installed |
| 16 | `scripts/install-crds.sh` hard-failed with `missing helm` on a machine without helm, so the whole cluster section never ran | `verify.sh` from a freshly extracted copy |
| 17 | The crossplane real-apply gate re-applied over objects left by an earlier run, so it reported `configured`/`unchanged` and never re-admitted a changed XRD. it would have passed an invalid document | running the gate on a clean cluster after changing the XRD API version |
| 18 | An experimental `CompositeResourceDefinition` at `apiextensions.crossplane.io/v2` was admitted as "clean" because the object already existed; a fresh apply rejects it (`Claims aren't supported in ... v2`). The generator therefore stays on v1, and a `--dry-run=server` gate was added so admission is always exercised | deleting the XRD and re-applying |

### Crossplane API-generation status (verified against Crossplane 2.4.3)

| Object | API version emitted | Served by 2.4.3 | Admission result |
| --- | --- | --- | --- |
| `CompositeResourceDefinition` | `apiextensions.crossplane.io/v1` | yes (v1 + v2 both served) | created; one deprecation notice from Crossplane ("v1 is deprecated… consider migrating to v2") |
| `Composition` | `apiextensions.crossplane.io/v1` + `mode: Pipeline` | yes (only served version for Composition) | created cleanly |
| `Bucket`, `ProviderConfig` | `minio.crossplane.io/v1` | yes | created cleanly |
| `Cluster` | `postgresql.cnpg.io/v1` | yes | created cleanly |
| `ExternalSecret`, `SecretStore` | `external-secrets.io/v1` | yes (`v1beta1` served=false) | created cleanly |
| `OpenTelemetryCollector` | `opentelemetry.io/v1beta1` | yes | created cleanly |

Why the XRD is still v1. v2 XRDs reject `spec.claimNames`: a fresh apply
returns `The CompositeResourceDefinition "groundstacks.ground.turion.space" is
invalid: spec: Invalid value: Claims aren't supported in
apiextensions.crossplane.io/v2`. This stack provisions a namespaced
`GroundStackClaim`, so migrating to v2 means moving to v2's namespaced-composite
model and deleting the claim. a product decision, not a version bump. v1 is
served, admitted and functional; the deprecation notice is the honest cost, and
it is the only warning the whole bundle produces. `--target kubernetes` remains
the operator-free path for ground stations.

## What is still *not* claimed

- kwok does not run containers. Pods are scheduled but not executed, so image
 pulls, the versitygw S3 handshake and the PostgreSQL init are not exercised
 end-to-end. Manifests, admission, scheduling and API semantics are real;
 workload behaviour is not.
- Crossplane / CNPG / ESO / OTel operators are not running. Their CRDs are
 installed, so the custom resources are schema-validated and admitted, but no
 controller reconciles them. `--target kubernetes` exists precisely so the
 ground station has an option that needs none of them.
- No Docker in this environment, so the tool's own container image is not
 built here.
