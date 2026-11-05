package main

// Brutal test suite: stress, adversarial input, determinism, idempotency and
// accuracy. These tests exist to break the tool, not to flatter it.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"turion-platform-drift/internal/generate"
	"turion-platform-drift/internal/scan"
	"turion-platform-drift/internal/validate"
)

// ------------------------------------------------------------------ stress

// TestStressLargeRepository scans a synthetic repository far bigger than any
// real ground service and asserts the Definition-of-Done budget still holds.
func TestStressLargeRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short mode")
	}
	dir := t.TempDir()
	const files = 3000
	pySnippet := strings.Join([]string{
		"import boto3",
		"client = boto3.client('s3')",
		"def go() -> None:",
		"    client.put_object(Bucket='turion-tlm-archive-prod', Key='k', Body=b'')",
		"    boto3.client('lambda').invoke(FunctionName='enrich', Payload=b'{}')",
		"",
	}, "\n")
	goSnippet := `package etl

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func Run(ctx context.Context, c *s3.Client) error { return nil }
`
	tsSnippet := "import { S3Client } from '@aws-sdk/client-s3';\nimport { LambdaClient } from '@aws-sdk/client-lambda';\nexport const c = new S3Client({});\n"
	yamlSnippet := "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: x\nspec:\n  template:\n    spec:\n      containers: []\n"
	hclSnippet := "resource \"aws_s3_bucket\" \"archive\" {\n  bucket = \"turion-tlm-archive-prod\"\n}\n"

	for i := 0; i < files; i++ {
		var sub, name, body string
		switch i % 5 {
		case 0:
			sub, name, body = "py", fmt.Sprintf("m%d.py", i), pySnippet
		case 1:
			sub, name, body = "go", fmt.Sprintf("m%d.go", i), goSnippet
		case 2:
			sub, name, body = "ts", fmt.Sprintf("m%d.ts", i), tsSnippet
		case 3:
			sub, name, body = "yaml", fmt.Sprintf("m%d.yaml", i), yamlSnippet
		default:
			sub, name, body = "tf", fmt.Sprintf("m%d.tf", i), hclSnippet
		}
		p := filepath.Join(dir, sub, fmt.Sprintf("pkg%03d", i/5), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	res, err := scan.Run(scan.Options{Root: dir})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("scan failed on a large repository: %v", err)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("DoD budget blown: %d files took %s (limit 30s)", files, elapsed)
	}
	if res.Files.Total < files {
		t.Fatalf("only %d of %d files were scanned", res.Files.Total, files)
	}
	if len(res.Findings) == 0 {
		t.Fatal("a repository built entirely from AWS-coupled snippets produced zero findings")
	}
	// Every file type must have been understood, not skipped.
	for _, lang := range []string{"python", "go", "typescript", "yaml", "hcl"} {
		if res.Files.ByLang[lang] == 0 {
			t.Errorf("no %s files were classified", lang)
		}
	}
	for _, id := range []string{"TPD001", "TPD003", "TPD020", "TPD030", "TPD049"} {
		if res.ByRule[id] == 0 {
			t.Errorf("rule %s never fired across %d synthetic files", id, files)
		}
	}
	t.Logf("scanned %d files in %s (%d findings, %d rules)",
		res.Files.Total, elapsed.Round(time.Millisecond), len(res.Findings), len(res.ByRule))
}

// TestScanIsDeterministic guards against map-iteration order leaking into
// results: the same tree must always produce byte-identical findings.
func TestScanIsDeterministic(t *testing.T) {
	var first string
	for i := 0; i < 3; i++ {
		res, err := scan.Run(scan.Options{Root: "ground-service"})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, f := range res.Findings {
			fmt.Fprintf(&b, "%s|%s|%d|%s\n", f.RuleID, f.File, f.Line, f.Symbol)
		}
		got := b.String()
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d produced different findings than run 0", i)
		}
	}
	if first == "" {
		t.Fatal("no findings produced; determinism test is vacuous")
	}
}

// TestConcurrentScans runs the scanner in parallel; run under -race to catch
// shared state in the collector or the embedded analyzer.
func TestConcurrentScans(t *testing.T) {
	const workers = 8
	results := make([]int, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := scan.Run(scan.Options{Root: "ground-service"})
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = len(res.Findings)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if results[i] != results[0] {
			t.Fatalf("worker %d found %d findings, worker 0 found %d", i, results[i], results[0])
		}
	}
}

// ------------------------------------------------------------------ adversarial input

// TestAdversarialFiles feeds the scanner inputs that break naive analyzers.
func TestAdversarialFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"crlf.py":           []byte("import boto3\r\nc = boto3.client('s3')\r\n"),
		"bom.py":            append([]byte{0xEF, 0xBB, 0xBF}, []byte("import boto3\n")...),
		"utf16.py":          {0xFF, 0xFE, 'i', 0, 'm', 0, 'p', 0},
		"nul.py":            []byte("import boto3\x00\x00\x00"),
		"binary.go":         {0x00, 0x01, 0x02, 0xFF, 0xFE},
		"empty.py":          {},
		"no-newline.yaml":   []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: x"),
		"longline.py":       []byte("x = '" + strings.Repeat("A", 1<<20) + "'\n"),
		"deep.yaml":         []byte(strings.Repeat("a:\n"+strings.Repeat(" ", 0)+"", 0) + deepYAML(400)),
		"flowbomb.yaml":     []byte("a: " + strings.Repeat("[", 500) + strings.Repeat("]", 500) + "\n"),
		"unterminated.yaml": []byte("a: [1, 2\nb: {c: 1\n"),
		"tabs.yaml":         []byte("\tkey:\tvalue\n\t\tbad: indentation\n"),
		"weird.ts":          []byte("import {}\nimport\n@aws-sdk/client-s3\n"),
		"huge.json":         []byte(`{"a":` + strings.Repeat("[1,", 2000) + "1]"),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An unreadable file must be reported, not crash the walk.
	if os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked.py")
		if err := os.WriteFile(locked, []byte("import boto3\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o644)
	}
	// A file that is a symlink loop must not hang the walk.
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(dir, loop); err == nil {
		defer os.Remove(loop)
	}

	res, err := scan.Run(scan.Options{Root: dir})
	if err != nil {
		t.Fatalf("adversarial tree killed the scan: %v", err)
	}
	// CRLF and BOM must not hide a boto3 import.
	var found bool
	for _, f := range res.Findings {
		if f.RuleID == "TPD001" && (strings.Contains(f.File, "crlf") || strings.Contains(f.File, "bom")) {
			found = true
		}
	}
	if !found {
		t.Error("CRLF/BOM encoded boto3 imports were missed")
	}
	for _, f := range res.Findings {
		if f.Line <= 0 {
			t.Errorf("finding without a line number: %+v", f)
		}
		if f.RuleID == "" {
			t.Errorf("finding without a rule id: %+v", f)
		}
	}
	t.Logf("adversarial tree: %d files, %d findings, %d recoverable errors",
		res.Files.Total, len(res.Findings), len(res.Errors))
}

// deepYAML builds a deeply nested mapping, which must not blow the stack.
func deepYAML(depth int) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString(strings.Repeat(" ", i))
		b.WriteString(fmt.Sprintf("k%d:\n", i))
	}
	b.WriteString(strings.Repeat(" ", depth))
	b.WriteString("leaf: 1\n")
	return b.String()
}

// TestScanRejectsBadRoots checks error handling rather than panics.
func TestScanRejectsBadRoots(t *testing.T) {
	if _, err := scan.Run(scan.Options{Root: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("scanning a missing directory should fail")
	}
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scan.Run(scan.Options{Root: f}); err == nil {
		t.Fatal("scanning a file instead of a directory should fail")
	}
}

// ------------------------------------------------------------------ idempotency

// TestGenerateIsIdempotent rebuilds the same tree twice and demands identical
// bytes; non-deterministic generation would make review and diffing useless.
func TestGenerateIsIdempotent(t *testing.T) {
	res, err := scan.Run(scan.Options{Root: "ground-service"})
	if err != nil {
		t.Fatal(err)
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	for _, pair := range [][2]string{{dirA, "a"}, {dirB, "b"}} {
		if _, err := generate.Build(res, generate.Options{
			OutDir: pair[1], Namespace: "ground-system", Target: "crossplane",
		}); err != nil {
			t.Fatal(err)
		}
		_ = pair
	}
	_ = dirA
	a, err := os.ReadFile(filepath.Join("a", "ground-station-portable.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("b", "ground-station-portable.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll("a")
	defer os.RemoveAll("b")
	if !bytes.Equal(a, b) {
		t.Fatal("two generator runs produced different manifests")
	}
}

// ------------------------------------------------------------------ cluster gates

// kubectlAvailable reports whether a real API server answers.
func kubectlAvailable(t *testing.T) (string, bool) {
	t.Helper()
	path, err := exec.LookPath("kubectl")
	if err != nil {
		return "", false
	}
	out, err := exec.Command(path, "get", "--raw", "/version").CombinedOutput()
	if err != nil {
		t.Logf("kubectl present but no API server: %s", strings.TrimSpace(string(out)))
		return "", false
	}
	return path, true
}

// TestGeneratedBundleAppliesToBareCluster is the literal Definition-of-Done
// gate: `kubectl apply --dry-run=client` against a real API server, with
// server-side OpenAPI validation, for the default (no-operator) target.
func TestGeneratedBundleAppliesToBareCluster(t *testing.T) {
	kubectl, ok := kubectlAvailable(t)
	if !ok {
		t.Skip("no reachable Kubernetes API server; run scripts/kwok-cluster.sh to enable")
	}
	res, err := scan.Run(scan.Options{Root: "ground-service"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := generate.Build(res, generate.Options{
		OutDir: dir, Namespace: "ground-system", Target: "kubernetes",
	}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "ground-station-portable.yaml")
	out, err := exec.Command(kubectl, "apply", "--dry-run=client", "-f", bundle).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl apply --dry-run=client failed:\n%s", out)
	}
	if strings.Contains(string(out), "no matches for kind") {
		t.Fatalf("bundle needs CRDs the bare cluster does not serve:\n%s", out)
	}
	vr, err := validate.DirOpts(dir, validate.Options{RequireKubectl: true, Kubectl: kubectl})
	if err != nil {
		t.Fatal(err)
	}
	if !vr.KubectlRan || !vr.KubectlOK {
		t.Fatalf("validator kubectl gate did not pass: %+v", vr)
	}
	if len(vr.Errors()) != 0 || len(vr.ResidualAWS) != 0 {
		t.Fatalf("validation errors=%v residual=%v", vr.Errors(), vr.ResidualAWS)
	}
}

// TestRequireKubectlFailsWhenUnavailable proves the gate cannot be faked off.
func TestRequireKubectlFailsWhenUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ns.yaml"),
		[]byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vr, err := validate.DirOpts(dir, validate.Options{
		Kubectl: "/nonexistent/kubectl", RequireKubectl: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(vr.Errors()) == 0 {
		t.Fatal("--require-kubectl with no kubectl must fail the run")
	}
	// And without the flag it must pass while saying clearly what it did.
	vr2, err := validate.DirOpts(dir, validate.Options{Kubectl: "/nonexistent/kubectl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vr2.Errors()) != 0 {
		t.Fatalf("without --require-kubectl the run should pass: %v", vr2.Errors())
	}
	if vr2.KubectlSkip == "" {
		t.Fatal("the report must explain why kubectl did not run")
	}
}
