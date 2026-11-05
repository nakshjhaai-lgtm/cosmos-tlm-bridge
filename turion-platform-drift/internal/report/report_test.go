package report

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
	"turion-platform-drift/internal/score"
	"turion-platform-drift/internal/validate"
)

func fixture() (*score.Scorecard, *validate.Report, []scan.Finding) {
	r1, _ := rules.Get("TPD001")
	r2, _ := rules.Get("TPD044")
	findings := []scan.Finding{
		{RuleID: "TPD001", Title: r1.Title, Severity: r1.Severity, Category: r1.Category,
			File: "ground-service/ingest/handler.py", Line: 12, Column: 5, Snippet: "aws batch submit"},
		{RuleID: "TPD044", Title: r2.Title, Severity: r2.Severity, Category: r2.Category,
			File: "ground-service/infra/main.tf", Line: 3, Snippet: "resource aws_s3_bucket"},
	}
	res := &scan.Result{
		Root: "ground-service", Findings: findings, Files: scan.FileStats{Total: 17, ByLang: map[string]int{"python": 9, "go": 5, "yaml": 3}},
		ByRule: map[string]int{"TPD001": 1, "TPD044": 1},
	}
	sc := score.Build(res)
	vr := &validate.Report{Files: 1, Objects: 1, KindCounts: map[string]int{"Namespace": 1}}
	return sc, vr, findings
}

func TestJSONIsStableAndParseable(t *testing.T) {
	sc, vr, findings := fixture()
	b, err := JSON(sc, vr, findings)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("JSON is not valid: %v", err)
	}
	for _, key := range []string{"tool", "version", "generated_at", "score", "grade", "counts", "categories", "findings", "validation"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
	if doc["grade"] != "F" && doc["grade"] != "D" && doc["grade"] != "C" && doc["grade"] != "B" && doc["grade"] != "A" {
		t.Errorf("unexpected grade %v", doc["grade"])
	}
	f, ok := doc["findings"].([]any)
	if !ok {
		t.Fatalf("findings should be an array, got %T", doc["findings"])
	}
	if len(f) != len(findings) {
		t.Errorf("findings = %d, want %d", len(f), len(findings))
	}
	if _, ok := doc["scorecard"].(map[string]any); !ok {
		t.Error("scorecard payload missing")
	}
	if _, ok := doc["counts"].(map[string]any); !ok {
		t.Error("counts mirror missing")
	}
}

func TestSARIFIsValid20(t *testing.T) {
	sc, _, findings := fixture()
	b, err := SARIF(sc, findings)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("SARIF is not valid JSON: %v", err)
	}
	if doc["version"] != "2.1.0" {
		t.Errorf("version = %v, want 2.1.0", doc["version"])
	}
	if doc["$schema"] == nil || !strings.Contains(doc["$schema"].(string), "sarif") {
		t.Error("missing SARIF $schema")
	}
	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("expected exactly one run, got %v", doc["runs"])
	}
	run := runs[0].(map[string]any)
	if run["tool"].(map[string]any)["driver"].(map[string]any)["name"] == "" {
		t.Error("driver name is empty")
	}
	results := run["results"].([]any)
	if len(results) != len(findings) {
		t.Fatalf("results = %d, want %d", len(results), len(findings))
	}
	first := results[0].(map[string]any)
	if first["ruleId"] != "TPD001" {
		t.Errorf("ruleId = %v", first["ruleId"])
	}
	if lvl := first["level"]; lvl != "error" && lvl != "warning" && lvl != "note" {
		t.Errorf("invalid SARIF level %v", lvl)
	}
	loc := first["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
	if loc["artifactLocation"].(map[string]any)["uri"] != "ground-service/ingest/handler.py" {
		t.Error("artifact uri not carried through")
	}
	if loc["region"].(map[string]any)["startLine"].(float64) != 12 {
		t.Error("startLine not carried through")
	}
	// every rule referenced by a result must be declared in the driver's rule list
	declared := map[string]bool{}
	for _, r := range run["tool"].(map[string]any)["driver"].(map[string]any)["rules"].([]any) {
		declared[r.(map[string]any)["id"].(string)] = true
	}
	for _, r := range results {
		if id, _ := r.(map[string]any)["ruleId"].(string); !declared[id] {
			t.Errorf("rule %s used but not declared", id)
		}
	}
}

func TestSARIFEmptyFindings(t *testing.T) {
	sc, _, _ := fixture()
	b, err := SARIF(sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"results"`) {
		t.Fatal("results key missing")
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	run := doc["runs"].([]any)[0].(map[string]any)
	results, ok := run["results"].([]any)
	if !ok {
		t.Fatalf("results should be an array, got %T", run["results"])
	}
	if len(results) != 0 {
		t.Error("expected zero results")
	}
	if _, ok := run["tool"].(map[string]any)["driver"].(map[string]any)["rules"].([]any); !ok {
		t.Fatal("rules should be an array even with no findings")
	}
}

func TestTextReportMentionsVerdict(t *testing.T) {
	sc, vr, _ := fixture()
	out := Text(sc, vr, 5)
	plain := ansiStrip(out)
	for _, want := range []string{"PORTABILITY SCORE", "TPD001", "FINDINGS", "CAPABILITY"} {
		if !strings.Contains(strings.ToUpper(plain), want) {
			t.Errorf("text report missing %q\n%s", want, plain)
		}
	}
	if strings.Contains(out, "\x1b[") && !strings.Contains(out, "\x1b[0m") {
		t.Error("ANSI escape without reset")
	}
}

func TestHTMLIsSelfContained(t *testing.T) {
	sc, vr, _ := fixture()
	h, err := HTML(sc, vr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(h)), "<!doctype html>") {
		t.Error("HTML should start with a doctype")
	}
	for _, bad := range []string{"http://", "https://cdn", "src=\"//"} {
		if strings.Contains(h, bad) {
			t.Errorf("HTML should not depend on the network (%q found)", bad)
		}
	}
	if !strings.Contains(h, "TPD001") {
		t.Error("HTML does not list findings")
	}
	for _, want := range []string{"<style", "score"} {
		if !strings.Contains(h, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

var ansiRe = regexp.MustCompile("\\x1b\\[[0-9;]*m")

func ansiStrip(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestLangSummary(t *testing.T) {
	got := langSummary(map[string]int{"python": 9, "go": 5})
	if !strings.Contains(got, "python") || !strings.Contains(got, "go") {
		t.Fatalf("langSummary = %q", got)
	}
	if langSummary(nil) == "" {
		t.Error("empty map should still render something")
	}
}
