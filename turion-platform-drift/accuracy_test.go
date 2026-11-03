package main

// Accuracy matrix: every rule must fire on a canonical snippet, and portable
// code must stay silent. This is the "zero false negatives" proof, expressed
// as one case per rule so a regression names the exact rule that broke.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
)

type probe struct {
	file string
	body string
}

// oneProbePerRule maps every rule ID to source that must trigger it.
func oneProbePerRule() map[string]probe {
	return map[string]probe{
		"TPD001": {"probes/tpd001/svc.py", "import boto3\n"},
		"TPD002": {"probes/tpd002/svc.py", "import boto3\nc = boto3.client('ec2')\n"},
		"TPD003": {"probes/tpd003/svc.py", "import boto3\nboto3.client('lambda').invoke(FunctionName='x', Payload=b'')\n"},
		"TPD004": {"probes/tpd004/batch/submit.py", "import boto3\nboto3.client('batch').submit_job(jobQueue='q', jobDefinition='d', jobName='n')\n"},
		"TPD005": {"probes/tpd005/svc.py", "bucket = 'turion-tlm-archive-prod'\n"},
		"TPD006": {"probes/tpd006/svc.py", "import boto3\nboto3.client('s3').put_object(Bucket='b', Key='k', Body=b'')\n"},
		"TPD007": {"probes/tpd007/svc.py", "ROLE = 'arn:aws:iam::123456789012:role/ground-etl'\n"},
		"TPD008": {"probes/tpd008/svc.py", "import boto3\nboto3.client('sts').assume_role(RoleArn='r', RoleSessionName='s')\n"},
		"TPD009": {"probes/tpd009/iam/policy.json", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"arn:aws:s3:::b"}]}`},
		"TPD010": {"probes/tpd010/svc.py", "import boto3\nboto3.client('sqs').send_message(QueueUrl='u', MessageBody='m')\n"},
		"TPD011": {"probes/tpd011/svc.py", "import boto3\nboto3.client('dynamodb').put_item(TableName='t', Item={})\n"},
		"TPD012": {"probes/tpd012/Makefile", "deploy:\n\taws s3 sync ./out s3://turion-tlm-archive-prod\n"},
		"TPD020": {"probes/tpd020/etl/main.go", "package m\n\nimport \"github.com/aws/aws-sdk-go-v2/service/s3\"\n\nvar _ = s3.Client{}\n"},
		"TPD021": {"probes/tpd021/etl/main.go", "package m\n\nimport (\n\t\"github.com/aws/aws-sdk-go-v2/aws\"\n\t\"github.com/aws/aws-sdk-go-v2/service/s3\"\n)\n\nfunc f(cfg aws.Config) { _ = s3.NewFromConfig(cfg) }\n"},
		"TPD030": {"probes/tpd030/ui/api.ts", "import { S3Client } from '@aws-sdk/client-s3';\nexport const c = new S3Client({});\n"},
		"TPD031": {"probes/tpd031/ui/api.ts", "import { LambdaClient, InvokeCommand } from '@aws-sdk/client-lambda';\nexport const c = new LambdaClient({});\n"},
		"TPD040": {"probes/tpd040/batch/job-definition.json", `{"jobDefinitionName":"droid-pass-processor","type":"container","containerProperties":{"image":"x"}}`},
		"TPD041": {"probes/tpd041/batch/compute-environment.json", `{"computeEnvironmentName":"droid-pass-compute","type":"MANAGED"}`},
		"TPD042": {"probes/tpd042/k8s/deploy.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\nspec:\n  template:\n    spec:\n      containers:\n        - name: c\n          image: 123456789012.dkr.ecr.us-west-2.amazonaws.com/turion/api:1\n"},
		"TPD043": {"probes/tpd043/k8s/deploy.yaml", "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: api\n  annotations:\n    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/api\n"},
		"TPD044": {"probes/tpd044/k8s/ingress.yaml", "apiVersion: networking.k8s.io/v1\nkind: Ingress\nmetadata:\n  name: api\n  annotations:\n    alb.ingress.kubernetes.io/scheme: internal\n    kubernetes.io/ingress.class: alb\nspec: {}\n"},
		"TPD045": {"probes/tpd045/k8s/pvc.yaml", "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data\nspec:\n  storageClassName: gp3\n  accessModes: [ReadWriteOnce]\n  resources:\n    requests:\n      storage: 1Gi\n"},
		"TPD046": {"probes/tpd046/k8s/deploy.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\nspec:\n  template:\n    spec:\n      volumes:\n        - name: secrets\n          csi:\n            driver: secrets-store.csi.k8s.io\n            volumeAttributes:\n              secretProviderClass: aws-secrets\n"},
		"TPD047": {"probes/tpd047/k8s/nodepool.yaml", "apiVersion: karpenter.k8s.aws/v1beta1\nkind: EC2NodeClass\nmetadata:\n  name: default\nspec: {}\n"},
		"TPD048": {"probes/tpd048/svc.py", "import boto3\nboto3.client('logs').put_log_events(logGroupName='g', logStreamName='s', logEvents=[])\n"},
		"TPD049": {"probes/tpd049/infra/main.tf", "resource \"aws_s3_bucket\" \"archive\" {\n  bucket = \"b\"\n}\n"},
	}
}

// portableCorpus is code that mentions cloud-adjacent words but has no AWS
// dependency. None of it may be flagged.
var portableCorpus = map[string]string{
	"portable/storage.py": `"""S3-compatible object store client.

Never hard-code an ARN, a bucket name literal or a region: read them from the
environment. This module must work against MinIO, VersityGW or any other
S3-compatible endpoint, so it deliberately avoids boto3's AWS-only defaults.
"""
import os

ENDPOINT = os.environ["OBJECT_STORE_ENDPOINT"]
BUCKET = os.environ["OBJECT_STORE_BUCKET"]


def put(key: str, body: bytes, client) -> None:
    # boto3 would work here, but we take an injected S3-compatible client.
    client.put_object(Bucket=BUCKET, Key=key, Body=body)
`,
	"portable/main.go": `package main

import "fmt"

// Uses an S3-compatible client injected at runtime; no AWS SDK import.
type Store interface{ Put(key string, body []byte) error }

func main() { fmt.Println("ground station ready") }
`,
	"portable/ui.ts": "// Calls the local telemetry service; no @aws-sdk imports anywhere.\n" +
		"export async function fetchTelemetry(channel: string): Promise<number> {\n" +
		"  const res = await fetch('/api/tlm/' + channel);\n" +
		"  return (await res.json()).value as number;\n" +
		"}\n",
	"portable/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: portable
  labels:
    app: portable
spec:
  selector:
    matchLabels:
      app: portable
  template:
    metadata:
      labels:
        app: portable
    spec:
      containers:
        - name: api
          image: registry.ground.local/portable/api:1
          envFrom:
            - configMapRef:
                name: objectstore-config
`,
	"portable/main.tf": `resource "kubernetes_namespace" "ground" {
  metadata {
    name = "ground-system"
  }
}
`,
}

func TestOneProbePerRuleFires(t *testing.T) {
	probes := oneProbePerRule()
	all := rules.All()
	if len(probes) != len(all) {
		t.Fatalf("the accuracy matrix has %d probes for %d rules: add the missing rules",
			len(probes), len(all))
	}
	dir := t.TempDir()
	for _, p := range probes {
		full := filepath.Join(dir, p.file)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := scan.Run(scan.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	fired := map[string]bool{}
	for _, f := range res.Findings {
		fired[f.RuleID] = true
	}
	for _, r := range all {
		if _, hasProbe := probes[r.ID]; !hasProbe {
			t.Errorf("rule %s has no probe", r.ID)
			continue
		}
		if !fired[r.ID] {
			t.Errorf("FALSE NEGATIVE: rule %s (%s) did not fire on its own probe", r.ID, r.Title)
		}
	}
}

func TestPortableCorpusIsSilent(t *testing.T) {
	dir := t.TempDir()
	for name, body := range portableCorpus {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := scan.Run(scan.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Findings {
		t.Errorf("FALSE POSITIVE: %s:%d %s %s", f.File, f.Line, f.RuleID, f.Symbol)
	}
	if res.Files.Total != len(portableCorpus) {
		t.Errorf("scanned %d of %d portable files", res.Files.Total, len(portableCorpus))
	}
}

// TestRuleCatalogIntegrity checks the metadata every report depends on.
func TestRuleCatalogIntegrityMatrix(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range rules.All() {
		if seen[r.ID] {
			t.Errorf("duplicate rule id %s", r.ID)
		}
		seen[r.ID] = true
		if !strings.HasPrefix(r.ID, "TPD") || len(r.ID) != 6 {
			t.Errorf("rule %q does not follow the TPDnnn convention", r.ID)
		}
		if r.Title == "" || r.Remediation == "" || r.Portability == "" || r.Description == "" {
			t.Errorf("rule %s is missing actionable metadata", r.ID)
		}
		switch r.Severity {
		case rules.SeverityCritical, rules.SeverityHigh, rules.SeverityMedium, rules.SeverityLow:
		default:
			t.Errorf("rule %s has an invalid severity %q", r.ID, r.Severity)
		}
		if r.Category == "" {
			t.Errorf("rule %s has no category", r.ID)
		}
		if r.Weight() <= 0 {
			t.Errorf("rule %s has a non-positive weight", r.ID)
		}
	}
}
