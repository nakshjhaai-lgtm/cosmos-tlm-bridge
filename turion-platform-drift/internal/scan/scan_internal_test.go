package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLangForClassification(t *testing.T) {
	cases := map[string]Language{
		"a.go": LangGo, "a.py": LangPython, "a.ts": LangTypeScript, "a.tsx": LangTypeScript,
		"a.js": LangTypeScript, "a.yaml": LangYAML, "a.yml": LangYAML, "a.json": LangJSON,
		"a.tf": LangHCL, "a.hcl": LangHCL, "a.jsonnet": LangJSONNet, "a.libsonnet": LangJSONNet,
		"a.sh": LangShell, "a.bash": LangShell, "Makefile": LangShell, "GNUmakefile": LangShell,
		"Dockerfile": LangShell, "containerfile": LangShell, "Jenkinsfile": LangShell,
		"app.Dockerfile": LangShell,
	}
	for name, want := range cases {
		got, ok := langFor(name)
		if !ok {
			t.Errorf("langFor(%q) returned not-ok", name)
			continue
		}
		if got != want {
			t.Errorf("langFor(%q) = %s, want %s", name, got, want)
		}
	}
	for _, name := range []string{"README.md", "go.mod", "image.png", "data.bin", "a.txt"} {
		if _, ok := langFor(name); ok {
			t.Errorf("langFor(%q) should not be scannable", name)
		}
	}
}

func TestExcluded(t *testing.T) {
	ex := []string{"/node_modules/", "/.git/"}
	if !excluded(filepath.Join("x", "node_modules", "a.js"), ex) {
		t.Error("node_modules should be excluded")
	}
	if excluded(filepath.Join("x", "src", "a.go"), ex) {
		t.Error("src should not be excluded")
	}
	if excluded("anything", []string{""}) {
		t.Error("an empty exclusion must not match everything")
	}
}

func TestCollectorDeduplicates(t *testing.T) {
	c := NewCollector()
	f := New("TPD001", "a.py", 3, LangPython, "import boto3", "s3", "m")
	c.Add(f)
	c.Add(f) // exact duplicate
	if got := len(c.Findings()); got != 1 {
		t.Fatalf("exact duplicates not collapsed: %d", got)
	}
	// Same rule, same line, overlapping symbol -> more specific one wins.
	c.Add(New("TPD001", "a.py", 3, LangPython, "import boto3 as b3", "s3", "m2"))
	fs := c.Findings()
	if len(fs) != 1 {
		t.Fatalf("overlapping symbols should collapse, got %d findings", len(fs))
	}
	if fs[0].Symbol != "import boto3 as b3" {
		t.Errorf("the more specific symbol should win, got %q", fs[0].Symbol)
	}
	// Same rule, same line, unrelated symbol -> two distinct findings.
	c.Add(New("TPD001", "a.py", 3, LangPython, "completely different", "s3", "m3"))
	if got := len(c.Findings()); got != 2 {
		t.Fatalf("unrelated findings on one line must both survive, got %d", got)
	}
}

func TestCollectorMerge(t *testing.T) {
	a, b := NewCollector(), NewCollector()
	a.Add(New("TPD001", "a.py", 1, LangPython, "s", "s3", "m"))
	b.Add(New("TPD002", "b.py", 2, LangPython, "s", "s3", "m"))
	a.AddError("boom")
	a.Merge(b)
	if got := len(a.Findings()); got != 2 {
		t.Fatalf("merge produced %d findings, want 2", got)
	}
	if len(a.Errors()) != 1 {
		t.Fatalf("errors not merged: %v", a.Errors())
	}
	a.Merge(nil) // must not panic
}

func TestTruncate(t *testing.T) {
	if got := Truncate("  short  ", 20); got != "short" {
		t.Errorf("Truncate trimmed incorrectly: %q", got)
	}
	long := strings.Repeat("x", 100)
	got := Truncate(long, 10)
	if len([]rune(got)) > 11 {
		t.Errorf("Truncate did not shorten: %q (len %d)", got, len(got))
	}
	if Truncate("a", 0) == "" {
		t.Error("Truncate with n=0 should not return empty for non-empty input")
	}
}

func TestReadFileLimitsAndErrors(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, MaxReadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(big); err == nil {
		t.Error("oversized files must be rejected")
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing files must return an error")
	}
	ok := filepath.Join(dir, "ok.py")
	if err := os.WriteFile(ok, []byte("import boto3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if content, err := ReadFile(ok); err != nil || !strings.Contains(content, "boto3") {
		t.Errorf("ReadFile failed on a normal file: %q %v", content, err)
	}
}

func TestPythonHeuristicFallback(t *testing.T) {
	c := NewCollector()
	scanPythonHeuristic("a.py", "import boto3\nc = boto3.client('s3')\n# boto3.client('lambda')\n", c)
	ids := map[string]bool{}
	for _, f := range c.Findings() {
		ids[f.RuleID] = true
		if f.RuleID == "TPD003" {
			t.Error("commented-out code must not be flagged by the fallback")
		}
	}
	if !ids["TPD001"] {
		t.Error("heuristic fallback missed the boto3 import")
	}
}

func TestNoPythonPathProducesFindings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.py"), []byte("import boto3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Options{Root: dir, NoPython: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.PythonAST {
		t.Error("--no-python should not report an AST pass")
	}
	if len(res.Findings) == 0 {
		t.Fatal("the regex fallback found nothing for a boto3 import")
	}
}

func TestRuleIDsAreSortedAndUnique(t *testing.T) {
	res := &Result{ByRule: map[string]int{"TPD002": 1, "TPD001": 1, "TPD020": 1}}
	ids := res.RuleIDs()
	if len(ids) == 0 {
		t.Fatal("no rules")
	}
	seen := map[string]bool{}
	for i, id := range ids {
		if seen[id] {
			t.Errorf("duplicate rule id %s", id)
		}
		seen[id] = true
		if i > 0 && ids[i-1] > id {
			t.Errorf("rule ids not sorted at %s", id)
		}
	}
}

func TestIsAWSPolicyPath(t *testing.T) {
	for _, p := range []string{"iam/policy.json", "policy.json", "iam/trust.json"} {
		if !IsAWSPolicyPath(p) {
			t.Errorf("IsAWSPolicyPath(%q) = false", p)
		}
	}
	for _, p := range []string{"main.go", "values.yaml", "notapolicy.txt"} {
		if IsAWSPolicyPath(p) {
			t.Errorf("IsAWSPolicyPath(%q) = true", p)
		}
	}
}

func TestScanGoSourceHandlesGarbage(t *testing.T) {
	c := NewCollector()
	ScanGoSource("bad.go", []byte("this is not go"), c)
	if len(c.Errors()) == 0 {
		t.Error("invalid Go should be reported as an error, not ignored")
	}
	// A parse error must not stop the scan of other files.
	c2 := NewCollector()
	ScanGoSource("good.go", []byte("package m\n\nimport \"github.com/aws/aws-sdk-go-v2/service/s3\"\n"), c2)
	if len(c2.Findings()) == 0 {
		t.Error("aws-sdk-go import was not detected")
	}
}
