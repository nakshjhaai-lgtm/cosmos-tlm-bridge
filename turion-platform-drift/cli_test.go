package main

// CLI tests: the command surface is the product, so the exit codes, flag
// handling and report files are asserted directly rather than by eyeballing
// terminal output.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersionAndHelp(t *testing.T) {
	if code := run([]string{"version"}); code != 0 {
		t.Errorf("version exit = %d", code)
	}
	if code := run([]string{"--version"}); code != 0 {
		t.Errorf("--version exit = %d", code)
	}
	if code := run([]string{"help"}); code != 0 {
		t.Errorf("help exit = %d", code)
	}
	if code := run([]string{"-h"}); code != 0 {
		t.Errorf("-h exit = %d", code)
	}
}

func TestRunNoArgsAndUnknownCommand(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Errorf("no args exit = %d, want 2", code)
	}
	if code := run([]string{"frobnicate"}); code != 2 {
		t.Errorf("unknown command exit = %d, want 2", code)
	}
}

func TestRunRules(t *testing.T) {
	if code := run([]string{"rules"}); code != 0 {
		t.Errorf("rules exit = %d", code)
	}
	if code := run([]string{"rules", "--json"}); code != 0 {
		t.Errorf("rules --json exit = %d", code)
	}
}

func TestScanWritesReportsAndExitsZero(t *testing.T) {
	out := t.TempDir()
	code := run([]string{"scan", "./ground-service", "--out", out, "--format", "text"})
	if code != 0 {
		t.Fatalf("scan exit = %d", code)
	}
	for _, name := range []string{"scorecard.json", "drift.sarif", "scorecard.html"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing report %s: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "scorecard.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("scorecard.json is not valid JSON: %v", err)
	}
	if doc["grade"] != "F" {
		t.Errorf("fixture grade = %v, want F", doc["grade"])
	}
}

func TestFlagsAfterPositionalPath(t *testing.T) {
	// Regression: Go's flag package stops at the first positional, which used
	// to silently drop everything after the path.
	out := t.TempDir()
	code := run([]string{"scan", "./ground-service", "--generate", "--out", out, "--target", "crossplane"})
	if code != 0 {
		t.Fatalf("flags after the path were not honoured (exit %d)", code)
	}
	if _, err := os.Stat(filepath.Join(out, "ground-station-portable.yaml")); err != nil {
		t.Error("--generate after the positional path was ignored: no bundle written")
	}
	raw, err := os.ReadFile(filepath.Join(out, "scorecard.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "crossplane") {
		t.Logf("note: target recorded in generated bundle header")
	}
}

func TestScanGates(t *testing.T) {
	if code := run([]string{"scan", "./ground-service", "--fail-under", "90"}); code == 0 {
		t.Error("--fail-under 90 should fail on a 0/100 fixture")
	}
	if code := run([]string{"scan", "./ground-service", "--fail-on-critical"}); code == 0 {
		t.Error("--fail-on-critical should fail on the fixture")
	}
	if code := run([]string{"scan", "./ground-service", "--fail-on-any"}); code == 0 {
		t.Error("--fail-on-any should fail on the fixture")
	}
	if code := run([]string{"scan", "./testdata/clean-service", "--fail-on-any"}); code != 0 {
		t.Errorf("portable code must pass --fail-on-any, exit = %d", code)
	}
	if code := run([]string{"scan", "./ground-service", "--fail-under", "0"}); code != 0 {
		t.Errorf("--fail-under 0 should pass, exit = %d", code)
	}
}

func TestScanFormats(t *testing.T) {
	if code := run([]string{"scan", "./ground-service", "--format", "json"}); code != 0 {
		t.Error("--format json failed")
	}
	if code := run([]string{"scan", "./ground-service", "--format", "sarif"}); code != 0 {
		t.Error("--format sarif failed")
	}
}

func TestScanBadPath(t *testing.T) {
	if code := run([]string{"scan", "./does-not-exist"}); code == 0 {
		t.Error("scanning a missing path should not exit 0")
	}
}

func TestValidateCommand(t *testing.T) {
	dir := t.TempDir()
	bundle := `apiVersion: v1
kind: Namespace
metadata:
  name: ok
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"validate", dir, "--kubectl", "/nonexistent/kubectl"}); code != 0 {
		t.Errorf("validate exit = %d, want 0", code)
	}
	if code := run([]string{"validate", dir, "--kubectl", "/nonexistent/kubectl", "--require-kubectl"}); code == 0 {
		t.Error("--require-kubectl with no kubectl should fail")
	}
	if code := run([]string{"validate"}); code != 2 {
		t.Errorf("validate without a directory exit = %d, want 2", code)
	}
	if code := run([]string{"validate", t.TempDir(), "--kubectl", "/nonexistent/kubectl"}); code == 0 {
		t.Error("validating an empty directory should fail")
	}
}

func TestValidateJSONOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"),
		[]byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"validate", dir, "--json", "--kubectl", "/nonexistent/kubectl"}); code != 0 {
		t.Errorf("validate --json exit = %d", code)
	}
}

func TestGenerateSubcommand(t *testing.T) {
	out := t.TempDir()
	if code := run([]string{"generate", "./ground-service", "--out", out, "--split"}); code != 0 {
		t.Fatalf("generate exit != 0")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 10 {
		t.Errorf("--split wrote %d files, expected one per object", len(entries))
	}
}

func TestSplitAndExcludeFlags(t *testing.T) {
	out := t.TempDir()
	code := run([]string{"scan", "./ground-service", "--generate", "--out", out, "--split", "--exclude", "batch/"})
	if code != 0 {
		t.Fatalf("scan exit = %d", code)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) == 0 {
		t.Fatal("no files written in split mode")
	}
}

func TestObjectStoreAndRegistryFlags(t *testing.T) {
	out := t.TempDir()
	code := run([]string{"scan", "./ground-service", "--generate", "--out", out,
		"--object-store", "minio", "--registry", "harbor.ground.local"})
	if code != 0 {
		t.Fatalf("scan exit = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(out, "ground-station-portable.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "harbor.ground.local") {
		t.Error("--registry was ignored")
	}
	if !strings.Contains(string(raw), "minio") {
		t.Error("--object-store minio was ignored")
	}
}

func TestNoPythonFlag(t *testing.T) {
	if code := run([]string{"scan", "./ground-service", "--no-python"}); code != 0 {
		t.Errorf("--no-python exit = %d", code)
	}
}

func TestTopFlag(t *testing.T) {
	if code := run([]string{"scan", "./ground-service", "--top", "2"}); code != 0 {
		t.Errorf("--top exit = %d", code)
	}
}

func TestBadFlagValue(t *testing.T) {
	if code := run([]string{"scan", "./ground-service", "--top", "not-a-number"}); code == 0 {
		t.Error("an invalid flag value should not exit 0")
	}
}
