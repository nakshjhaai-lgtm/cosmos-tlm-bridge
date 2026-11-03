# ground-service

Turion ground-segment telemetry ETL and API.

> **This baseline is AWS-coupled on purpose.** It is the reference input for
> `turion-platform-drift scan ./ground-service`, which must detect 100% of the
> couplings below and generate portable replacements.

## AWS dependencies present

| Area | Where |
| --- | --- |
| boto3 / botocore SDK | `batch/`, `lambda/`, `storage/` |
| AWS Batch job definition + compute env + queue | `batch/*.json`, `batch/job-queue.yaml` |
| AWS Lambda invocation | `lambda/invoke_telemetry_enrich.py`, `ui/src/api.ts`, `etl/main.go` |
| S3 primitives + hard-coded buckets | `storage/archive_tlm.py`, `batch/` |
| AWS IAM policy document | `iam/policy.json` |
| STS AssumeRole / IRSA | `batch/`, `k8s/`, `etl/main.go` |
| ECR images | `k8s/`, `helm/` |
| EBS gp3 storage class | `k8s/ground-service-deployment.yaml` |
| Secrets Manager / SSM CSI | `k8s/secrets-provider-class.yaml` |
| AWS Load Balancer ingress | `k8s/ground-service-deployment.yaml` |
| Karpenter EC2NodeClass | `k8s/karpenter-nodepool.yaml` |
| CloudWatch / X-Ray exporters | `telemetry/otel-collector.yaml` |
| SQS / SNS / EventBridge / DynamoDB | `storage/`, `lambda/` |
| Terraform aws_* resources | `infra/main.tf` |
| AWS CLI shell-outs | `Makefile`, `batch/submit_pass_processing.py` |
