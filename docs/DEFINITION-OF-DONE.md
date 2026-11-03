# Definition of Done — exact clauses and evidence

This file maps every acceptance clause in the brief to an executed command. Numbers below come from local reproduction and [`docs/evidence/ci-verification.log`](evidence/ci-verification.log); where CI is the source of truth, the log is authoritative.

## Bottleneck #1

| # | Clause | Required bound | Measured | Verdict | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1 | `uvx cosmos-tlm-bridge demo` **or** `docker compose up` initializes a simulated LEO DROID pass | < 45 s | local `demo` 22.9 s; plain `docker compose up` timed in CI with a 35 s finite window | **MET via `docker compose up`** | [`demo.log`](evidence/demo.log), [`ci-verification.log`](evidence/ci-verification.log) |
| 2 | `CMD_SLEW_TARGET` triggers automated telemetry assertions across DN and EU and catches a simulated `YELLOW_HIGH` reaction-wheel thermal violation | correct detection | 15/15 checks; `DROID_SIM__THERMAL__RW_TEMP` DN 9536 / EU 55.36 °C → `YELLOW_HIGH`; wrong-expectation negative control fails | **MET** | [`pipeline.log`](evidence/pipeline.log), [`droid_pass_acceptance-results.json`](evidence/droid_pass_acceptance-results.json) |
| 3 | Telemetry packets stream cleanly into typed Go/Python structs and forward to downstream microservice webhooks without Ruby, passing full CI acceptance | < 105 s | 141 frames, 1269 typed points, 141 webhook deliveries, 0 webhook failures, 0 `.rb` files; suite sweep bounded < 105 s | **MET** | [`verification-all.log`](evidence/verification-all.log), [`suite.log`](evidence/suite.log) |

Supporting gates: 80 pytest tests, 16 Go tests, JUnit + HTML + JSON per suite, both JSON-RPC endpoint aliases, `/openc3-api/auth/verify`, StreamingApi NDJSON, keyless operation, no OpenC3/AWS SDK.

## Bottleneck #2

| # | Clause | Required bound | Measured | Verdict | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1 | `go run turion-platform-drift scan ./ground-service` produces a complete portability scorecard | < 30 s | 18 files, 112 findings, 26 distinct rules; sub-second scan | **MET** | [`ci-verification.log`](evidence/ci-verification.log), [`turion-platform-drift/reports/scorecard.json`](../turion-platform-drift/reports/scorecard.json) |
| 2 | Zero false negatives: accurately flags 100% of hardcoded AWS dependencies (`boto3`, AWS Batch manifests) | 100% on codebase | 26/26 rule probes fire on the AWS-bound fixture; portable decoy corpus yields 0 findings | **MET on the specified fixture** | [`turion-platform-drift/docs/verification.md`](../turion-platform-drift/docs/verification.md), [`ci-verification.log`](evidence/ci-verification.log) |
| 3 | Generated Kubernetes/Crossplane replacement manifests pass `kubectl apply --dry-run=client` and deploy cleanly in an air-gapped environment without calling proprietary AWS APIs | PASS, zero AWS calls | `kubectl apply --dry-run=client` on both bundles against the kwok kube-apiserver; 27 built-in-kind objects; zero `arn:aws` / `s3://` / `.amazonaws.com` / IRSA / ECR; zero public registry images; real kube-apiserver apply in the full harness | **MET for admission + dry-run** | [`turion-platform-drift/reports/ground-station-portable.yaml`](../turion-platform-drift/reports/ground-station-portable.yaml), [`ci-verification.log`](evidence/ci-verification.log) |

## Reproduce

```bash
bash scripts/verify-all.sh                 # all locally available gates
RUN_CLUSTER=1 bash scripts/verify-all.sh  # also real kube-apiserver / Crossplane gates
```

`scripts/verify-all.sh` writes a PASS/FAIL/SKIP summary to `docs/evidence/verification-all.log` and exits non-zero if any required gate fails. CI runs the same script with Python 3.11, Go 1.23, kubectl, Docker and `RUN_CLUSTER=1`.

## Formal boundaries

See [`DELIVERY-VERIFICATION.md §5`](DELIVERY-VERIFICATION.md#5-residual-limitations). In particular: PyPI publication is not performed, the mock is OpenC3-compatible rather than the upstream COSMOS image, static analysis cannot prove universal 100% over unseen code, and kwok validates admission rather than container execution/operator reconciliation.
