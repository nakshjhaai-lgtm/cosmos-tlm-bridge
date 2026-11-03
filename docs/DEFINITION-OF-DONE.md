# Definition of done: clauses and evidence

This file maps each acceptance clause to a command that ran. The numbers come from GitHub Actions run `41670286164` (`docs/evidence/ci-verification.log`): 23 passed, 0 failed, 0 skipped, with the linter's real-cluster harness reporting 33 passed and 0 failed. Local runs agree.

1. First bottleneck: `cosmos-tlm-bridge`

| # | Clause | Required bound | Measured | Verdict | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1 | `uvx cosmos-tlm-bridge demo` or `docker compose up` starts a simulated LEO DROID pass | under 45 s | CI `demo` 23.595 s; plain `docker compose up` 36.976 s with a 30 s finite window | met through `docker compose up` | `docs/evidence/demo.log`, `docs/evidence/ci-verification.log` |
| 2 | `CMD_SLEW_TARGET` triggers DN and EU telemetry assertions and catches a simulated `YELLOW_HIGH` reaction-wheel thermal violation | correct detection | 15/15 checks; `DROID_SIM__THERMAL__RW_TEMP` DN 9536 and EU 55.36 C produced `YELLOW_HIGH`; a wrong-expectation negative control fails | met | `docs/evidence/pipeline.log`, `docs/evidence/droid_pass_acceptance-results.json` |
| 3 | Packets stream into typed Go/Python structs and reach downstream webhooks without Ruby, with full CI acceptance | under 105 s | 141 frames, 1269 typed points, 141 webhook deliveries, 0 webhook failures, 0 `.rb` files; CI suite sweep 27.051 s | met | `docs/evidence/verification-all.log`, `docs/evidence/suite.log` |

Supporting gates: 80 pytest tests, 16 Go tests, JUnit, HTML, and JSON per suite, both JSON-RPC endpoint aliases, `/openc3-api/auth/verify`, StreamingApi NDJSON, keyless operation, and no OpenC3 or AWS SDK.

2. Second bottleneck: `turion-platform-drift`

| # | Clause | Required bound | Measured | Verdict | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1 | `go run turion-platform-drift scan ./ground-service` produces a complete portability scorecard | under 30 s | 0.220 s in CI; 18 files, 112 findings, 26 distinct rules | met | `docs/evidence/ci-verification.log`, `turion-platform-drift/reports/scorecard.json` |
| 2 | Zero false negatives: flags 100% of hardcoded AWS dependencies (`boto3`, AWS Batch manifests) | 100% on the codebase | 26/26 rule probes fire on the AWS-bound fixture; the portable decoy corpus yields 0 findings | met on the specified fixture | `turion-platform-drift/docs/verification.md`, `docs/evidence/ci-verification.log` |
| 3 | Generated Kubernetes and Crossplane manifests pass `kubectl apply --dry-run=client` and deploy in an air-gapped environment without proprietary AWS APIs | pass, zero AWS calls | `kubectl apply --dry-run=client` took 917 ms and 908 ms; server dry-runs and real apply also passed against the kwok kube-apiserver; 27 built-in-kind objects; zero `arn:aws`, `s3://`, `.amazonaws.com`, IRSA, ECR, or public registry references | met for admission and dry-run | `turion-platform-drift/reports/ground-station-portable.yaml`, `docs/evidence/ci-verification.log` |

3. Reproduce

```bash
bash scripts/verify-all.sh
RUN_CLUSTER=1 bash scripts/verify-all.sh
```

The first command runs every gate that works on the local machine. The second also starts a real kube-apiserver and Crossplane gate. `scripts/verify-all.sh` writes a PASS/FAIL/SKIP summary to `docs/evidence/verification-all.log` and exits non-zero if a required gate fails. CI runs the same script with Python 3.11, Go 1.23, kubectl, Docker, and `RUN_CLUSTER=1`.

4. Formal boundaries

See `DELIVERY-VERIFICATION.md#6-residual-limitations`. PyPI publication is not performed. The mock is OpenC3-compatible rather than the upstream COSMOS image. Static analysis cannot prove a universal 100% result over unseen code. kwok validates admission rather than container execution or operator reconciliation.
