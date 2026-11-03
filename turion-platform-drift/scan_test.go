package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
)

// ruleIDs returns the set of rule IDs present in a scan result.
func ruleIDs(res *scan.Result) map[string]bool {
	out := map[string]bool{}
	for _, f := range res.Findings {
		out[f.RuleID] = true
	}
	return out
}

func mustScan(t *testing.T, root string) *scan.Result {
	t.Helper()
	res, err := scan.Run(scan.Options{Root: root})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	return res
}

// TestZeroFalseNegatives is the Definition-of-Done gate: every hard-coded AWS
// dependency planted in the ground-service fixture must be detected.
func TestZeroFalseNegatives(t *testing.T) {
	res := mustScan(t, "ground-service")
	got := ruleIDs(res)

	want := []string{
		"TPD001", // boto3 import
		"TPD002", // boto3 client
		"TPD003", // Lambda invoke
		"TPD004", // AWS Batch submission
		"TPD005", // hard-coded S3 bucket
		"TPD006", // S3 primitives
		"TPD007", // arn:aws literal
		"TPD008", // STS assume role
		"TPD009", // IAM policy document
		"TPD010", // SQS/SNS/EventBridge
		"TPD011", // DynamoDB / RDS
		"TPD012", // AWS CLI shell-out
		"TPD020", // aws-sdk-go import
		"TPD021", // aws-sdk-go client
		"TPD030", // @aws-sdk import
		"TPD031", // Lambda client in TS
		"TPD040", // Batch job definition
		"TPD041", // Batch compute environment
		"TPD042", // ECR image
		"TPD043", // IRSA annotation
		"TPD044", // ALB ingress
		"TPD045", // EBS storage class
		"TPD046", // Secrets Manager CSI
		"TPD047", // Karpenter EC2NodeClass
		"TPD048", // CloudWatch/X-Ray
		"TPD049", // Terraform aws_*
	}
	var missing []string
	for _, id := range want {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("missed %d/%d planted rules: %v", len(missing), len(want), missing)
	}
	t.Logf("detected %d/%d planted AWS couplings across %d files, %d findings",
		len(want), len(want), res.Files.Total, len(res.Findings))
}

// TestPerFileDetection asserts the rule fires on the specific file, so a
// rewrite of the fixture cannot silently satisfy the set-level test.
func TestPerFileDetection(t *testing.T) {
	res := mustScan(t, "ground-service")
	type key struct{ rule, file string }
	have := map[key]bool{}
	for _, f := range res.Findings {
		have[key{f.RuleID, f.File}] = true
	}
	cases := []key{
		{"TPD001", "batch/submit_pass_processing.py"},
		{"TPD003", "lambda/invoke_telemetry_enrich.py"},
		{"TPD004", "batch/submit_pass_processing.py"},
		{"TPD005", "storage/archive_tlm.py"},
		{"TPD006", "storage/archive_tlm.py"},
		{"TPD008", "batch/submit_pass_processing.py"},
		{"TPD009", "iam/policy.json"},
		{"TPD012", "Makefile"},
		{"TPD020", "etl/main.go"},
		{"TPD030", "ui/src/api.ts"},
		{"TPD031", "ui/src/api.ts"},
		{"TPD040", "batch/job-definition.json"},
		{"TPD041", "batch/compute-environment.json"},
		{"TPD042", "helm/ground-service/values.yaml"},
		{"TPD043", "k8s/ground-service-deployment.yaml"},
		{"TPD044", "k8s/ground-service-deployment.yaml"},
		{"TPD045", "k8s/ground-service-deployment.yaml"},
		{"TPD046", "k8s/secrets-provider-class.yaml"},
		{"TPD047", "k8s/karpenter-nodepool.yaml"},
		{"TPD048", "telemetry/otel-collector.yaml"},
		{"TPD049", "infra/main.tf"},
		{"TPD004", "batch/job-queue.yaml"},
	}
	for _, c := range cases {
		if !have[c] {
			t.Errorf("expected %s in %s", c.rule, c.file)
		}
	}
}

// TestNoFalsePositivesOnPortableCode is the other half of accuracy: portable
// code that merely *mentions* AWS (comments, docstrings, migration notes) must
// produce zero findings.
func TestNoFalsePositivesOnPortableCode(t *testing.T) {
	res := mustScan(t, "testdata/clean-service")
	if len(res.Findings) != 0 {
		var lines []string
		for _, f := range res.Findings {
			lines = append(lines, f.RuleID+" "+f.File+":"+itoa(f.Line)+" "+f.Symbol)
		}
		t.Fatalf("expected zero findings on portable code, got %d:\n  %s",
			len(res.Findings), strings.Join(lines, "\n  "))
	}
	if res.Files.Total == 0 {
		t.Fatal("fixture was not scanned at all")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestEdgeCasePythonForms covers aliasing, from-imports, dynamic service names
// and chained calls.
func TestEdgeCasePythonForms(t *testing.T) {
	res := mustScan(t, "testdata/edge-cases")
	got := ruleIDs(res)
	for _, id := range []string{"TPD001", "TPD002", "TPD011", "TPD010"} {
		if !got[id] {
			t.Errorf("edge cases missed %s (got %v)", id, keys(got))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestScanIsFast(t *testing.T) {
	res := mustScan(t, "ground-service")
	if res.Duration > 30 {
		t.Fatalf("scan took %.2fs, budget is 30s", res.Duration)
	}
	t.Logf("scan of %d files completed in %.3fs", res.Files.Total, res.Duration)
}

func TestScannerSkipsVendorAndNodeModules(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "node_modules", "aws-sdk")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "index.js"),
		[]byte(`const x = require("aws-sdk");`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"),
		[]byte("import boto3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := mustScan(t, dir)
	for _, f := range res.Findings {
		if strings.Contains(f.File, "node_modules") {
			t.Fatalf("scanned an excluded path: %s", f.File)
		}
	}
	if !ruleIDs(res)["TPD001"] {
		t.Fatal("missed the real finding outside node_modules")
	}
}

func TestEveryFindingMapsToACatalogRule(t *testing.T) {
	res := mustScan(t, "ground-service")
	for _, f := range res.Findings {
		if _, ok := rules.Get(f.RuleID); !ok {
			t.Errorf("finding references unknown rule %q", f.RuleID)
		}
		if f.Remediation == "" {
			t.Errorf("%s has no remediation text", f.RuleID)
		}
	}
}

func TestRuleCatalogIntegrity(t *testing.T) {
	all := rules.All()
	if len(all) < 25 {
		t.Fatalf("rule catalog unexpectedly small: %d", len(all))
	}
	ids := map[string]bool{}
	for _, r := range all {
		if ids[r.ID] {
			t.Errorf("duplicate rule id %s", r.ID)
		}
		ids[r.ID] = true
		if r.Title == "" || r.Description == "" || r.Remediation == "" || r.Portability == "" {
			t.Errorf("%s is missing catalogue fields", r.ID)
		}
		if r.Weight() <= 0 {
			t.Errorf("%s has non-positive weight", r.ID)
		}
	}
}

func TestPythonASTActuallyUsed(t *testing.T) {
	res := mustScan(t, "ground-service")
	if !res.PythonAST {
		t.Skip("python3 not available; heuristic fallback exercised instead")
	}
	// The AST path attributes Lambda invocation to the invoke() call itself.
	var found bool
	for _, f := range res.Findings {
		if f.RuleID == "TPD003" && strings.Contains(f.Symbol, "invoke") {
			found = true
		}
	}
	if !found {
		t.Error("AST analysis did not attribute the lambda invoke call")
	}
}
