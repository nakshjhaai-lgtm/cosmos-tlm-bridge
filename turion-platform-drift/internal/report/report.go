// Package report renders the portability scorecard as text, JSON, SARIF, or
// a standalone HTML report.
package report

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
	"turion-platform-drift/internal/score"
	"turion-platform-drift/internal/validate"
)

const (
	reset = "\033[0m"
	bold  = "\033[1m"
	dim   = "\033[2m"
	red   = "\033[31m"
	yel   = "\033[33m"
	grn   = "\033[32m"
	cyn   = "\033[36m"
	mag   = "\033[35m"
)

var noColor = os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"

func c(s, color string) string {
	if noColor {
		return s
	}
	return color + s + reset
}

func sevColor(s rules.Severity) string {
	switch s {
	case rules.SeverityCritical:
		return red
	case rules.SeverityHigh:
		return yel
	case rules.SeverityMedium:
		return cyn
	default:
		return dim
	}
}

func gradeColor(g score.Grade) string {
	switch g {
	case score.GradeA:
		return grn
	case score.GradeB:
		return cyn
	case score.GradeC:
		return yel
	default:
		return red
	}
}

// Text renders the human scorecard.
func Text(sc *score.Scorecard, vr *validate.Report, top int) string {
	var b strings.Builder

	line := func(s string) { b.WriteString(s + "\n") }

	line("")
	line(c("  turion-platform-drift", bold) + c("  ·  cloud-agnostic ground-station portability", dim))
	line(c("  ─────────────────────────────────────────────────────────────────────────", dim))
	line(fmt.Sprintf("  root      %s", sc.Root))
	line(fmt.Sprintf("  scanned   %d files  (%s)  in %.2fs",
		sc.FilesScanned, langSummary(sc.Languages), sc.DurationSec))
	pyAST := "regex fallback"
	if sc.PythonAST {
		pyAST = "python ast"
	}
	line(fmt.Sprintf("  analysis  go ast · %s · yaml/json manifests", pyAST))
	line("")

	// Score banner.
	gc := gradeColor(sc.Grade)
	line(fmt.Sprintf("  %s  %s  %s  %s",
		c(fmt.Sprintf("PORTABILITY SCORE  %3d/100", sc.Score), bold),
		c(score.Bar(sc.Score, 34), gc),
		c(string(sc.Grade), gc+bold),
		""))
	line(fmt.Sprintf("  %s", c(sc.Verdict, dim)))
	line("")

	// Severity tally.
	line(fmt.Sprintf("  findings  %s  critical   %s  high   %s  medium   %s  low   %s",
		c(fmt.Sprintf("%d", sc.TotalFindings), bold),
		c(fmt.Sprintf("%d", sc.CriticalCount), red),
		c(fmt.Sprintf("%d", sc.HighCount), yel),
		c(fmt.Sprintf("%d", sc.MediumCount), cyn),
		c(fmt.Sprintf("%d", sc.LowCount), dim)))
	line("")

	// Category breakdown.
	if len(sc.Categories) > 0 {
		line("  " + c("COUPLING BY CAPABILITY", bold))
		line("  " + c("───────────────────────────────────────────────────────────────────────", dim))
		for _, cat := range sc.Categories {
			line(fmt.Sprintf("    %-16s %3d finding(s)  %s",
				string(cat.Category),
				cat.Findings,
				c(score.Bar(int(100-cat.Penalty), 18), yel)))
		}
		line("")
	}

	// Rule table.
	if len(sc.ByRule) > 0 {
		line("  " + c("RULES TRIGGERED", bold))
		line("  " + c("───────────────────────────────────────────────────────────────────────", dim))
		for _, r := range sc.ByRule {
			line(fmt.Sprintf("    %s %-9s %3d×  %s",
				c(r.RuleID, bold),
				c(string(r.Severity), sevColor(r.Severity)),
				r.Count,
				r.Title))
			line(fmt.Sprintf("      %s %s", c("→", dim), c(r.Remediation, dim)))
		}
		line("")
	}

	// Hotspots.
	if top > 0 && len(sc.TopHotspots) > 0 {
		n := top
		if n > len(sc.TopHotspots) {
			n = len(sc.TopHotspots)
		}
		line("  " + c(fmt.Sprintf("TOP %d HOTSPOTS", n), bold))
		line("  " + c("───────────────────────────────────────────────────────────────────────", dim))
		for _, f := range sc.TopHotspots[:n] {
			line(fmt.Sprintf("    %s:%s  %s  %s",
				c(f.File, cyn), c(fmt.Sprint(f.Line), dim),
				c(f.RuleID, bold), scan.Truncate(f.Symbol, 62)))
			portability := f.Portability
			if portability == "" {
				if r, ok := rules.Get(f.RuleID); ok {
					portability = r.Portability
				}
			}
			if portability != "" {
				line(fmt.Sprintf("      %s %s", c("→", dim), c(portability, dim)))
			}
		}
		line("")
	}

	// Generation.
	if len(sc.Generated) > 0 {
		line("  " + c("GENERATED PORTABLE REPLACEMENTS", bold))
		line("  " + c("───────────────────────────────────────────────────────────────────────", dim))
		for _, g := range sc.Generated {
			line("    " + c("+", grn) + " " + g)
		}
		line("")
	}

	// Validation.
	if vr != nil && vr.Objects > 0 {
		line("  " + c("AIR-GAPPED MANIFEST VALIDATION", bold))
		line("  " + c("───────────────────────────────────────────────────────────────────────", dim))
		kinds := make([]string, 0, len(vr.KindCounts))
		for k := range vr.KindCounts {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		parts := make([]string, 0, len(kinds))
		for _, k := range kinds {
			parts = append(parts, fmt.Sprintf("%s×%d", k, vr.KindCounts[k]))
		}
		line(fmt.Sprintf("    parsed     %d object(s) across %d file(s)", vr.Objects, vr.Files))
		line(fmt.Sprintf("    kinds      %s", strings.Join(parts, "  ")))
		verdict := c("PASS", grn)
		if len(vr.Errors()) > 0 {
			verdict = c("FAIL", red)
		}
		line(fmt.Sprintf("    schema     %s  (%d error, %d warning)",
			verdict, len(vr.Errors()), len(vr.Issues)-len(vr.Errors())))
		if vr.KubectlRan {
			kv := c("PASS", grn)
			if !vr.KubectlOK {
				kv = c("FAIL", red)
			}
			line(fmt.Sprintf("    kubectl    %s  %s", kv, c(vr.KubectlCmd, dim)))
		} else {
			line(fmt.Sprintf("    kubectl    %s", c("skipped: "+vr.KubectlSkip, dim)))
		}
		if len(vr.ResidualAWS) > 0 {
			line(fmt.Sprintf("    residual   %s", c(fmt.Sprintf("%d AWS reference(s) survived generation", len(vr.ResidualAWS)), red)))
			for i, r := range vr.ResidualAWS {
				if i >= 5 {
					break
				}
				line(fmt.Sprintf("               %s: %s", r.File, r.Message))
			}
		} else {
			line(fmt.Sprintf("    residual   %s", c("none — zero proprietary AWS references", grn)))
		}
		line("")
	}

	if len(sc.Errors) > 0 {
		line("  " + c("SCAN NOTES", bold))
		for i, e := range sc.Errors {
			if i >= 5 {
				line(fmt.Sprintf("    %s", c(fmt.Sprintf("… and %d more", len(sc.Errors)-5), dim)))
				break
			}
			line("    " + c("! "+e, dim))
		}
		line("")
	}

	return b.String()
}

func langSummary(m map[string]int) string {
	if len(m) == 0 {
		return "no supported files"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
	}
	return strings.Join(parts, ", ")
}

// Version is the report schema/tool version stamped into every document.
const Version = "1.0.0"

// JSON renders the machine-readable scorecard.
// JSON renders the machine-readable scorecard. The document is stable enough
// for CI to consume with jq: every collection is present and is never null.
func JSON(sc *score.Scorecard, vr *validate.Report, findings []scan.Finding) ([]byte, error) {
	if findings == nil {
		findings = []scan.Finding{}
	}
	if sc != nil {
		if sc.Categories == nil {
			sc.Categories = []score.CategoryScore{}
		}
		if sc.ByRule == nil {
			sc.ByRule = []score.RuleBucket{}
		}
		if sc.TopHotspots == nil {
			sc.TopHotspots = []scan.Finding{}
		}
		if sc.Languages == nil {
			sc.Languages = map[string]int{}
		}
	}

	doc := map[string]any{
		"tool":         "turion-platform-drift",
		"version":      Version,
		"generated_at": Now(),
		"scorecard":    sc,
		"findings":     findings,
	}
	if sc != nil {
		// Convenience mirrors so `jq .score` works without reaching in.
		doc["score"] = sc.Score
		doc["grade"] = sc.Grade
		doc["counts"] = map[string]any{
			"total": sc.TotalFindings, "critical": sc.CriticalCount,
			"high": sc.HighCount, "medium": sc.MediumCount, "low": sc.LowCount,
			"distinct_rules": sc.DistinctRules, "files_scanned": sc.FilesScanned,
		}
		doc["categories"] = sc.Categories
	}
	if vr != nil {
		kinds := vr.KindCounts
		if kinds == nil {
			kinds = map[string]int{}
		}
		issues := vr.Issues
		if issues == nil {
			issues = []validate.Issue{}
		}
		residual := vr.ResidualAWS
		if residual == nil {
			residual = []validate.Issue{}
		}
		skipped := vr.Skipped
		if skipped == nil {
			skipped = []string{}
		}
		doc["validation"] = map[string]any{
			"files":        vr.Files,
			"objects":      vr.Objects,
			"kinds":        kinds,
			"issues":       issues,
			"residual_aws": residual,
			"skipped":      skipped,
			"ok":           vr.OK(),
			"kubectl": map[string]any{
				"ran":     vr.KubectlRan,
				"ok":      vr.KubectlOK,
				"cmd":     vr.KubectlCmd,
				"skipped": vr.KubectlSkip,
				"output":  vr.KubectlOutput,
			},
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// SARIF renders a SARIF 2.1.0 log for GitHub code scanning.
func SARIF(sc *score.Scorecard, findings []scan.Finding) ([]byte, error) {
	type sarifRule struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		ShortDescription struct {
			Text string `json:"text"`
		} `json:"shortDescription"`
		FullDescription struct {
			Text string `json:"text"`
		} `json:"fullDescription"`
		Help struct {
			Text     string `json:"text"`
			Markdown string `json:"markdown"`
		} `json:"help"`
		DefaultConfiguration struct {
			Level string `json:"level"`
		} `json:"defaultConfiguration"`
		Properties map[string]any `json:"properties"`
	}

	levels := map[rules.Severity]string{
		rules.SeverityCritical: "error",
		rules.SeverityHigh:     "error",
		rules.SeverityMedium:   "warning",
		rules.SeverityLow:      "note",
	}

	seen := map[string]bool{}
	sRules := []sarifRule{}
	for _, f := range findings {
		if seen[f.RuleID] {
			continue
		}
		seen[f.RuleID] = true
		r, _ := rules.Get(f.RuleID)
		var sr sarifRule
		sr.ID = f.RuleID
		sr.Name = strings.ReplaceAll(r.Title, " ", "")
		sr.ShortDescription.Text = r.Title
		sr.FullDescription.Text = r.Description
		sr.Help.Text = r.Remediation
		sr.Help.Markdown = "**Portable replacement:** " + r.Portability + "\n\n" + r.Remediation
		sr.DefaultConfiguration.Level = levels[r.Severity]
		sr.Properties = map[string]any{
			"category": string(r.Category), "severity": string(r.Severity),
			"portability": r.Portability,
		}
		sRules = append(sRules, sr)
	}
	sort.Slice(sRules, func(i, j int) bool { return sRules[i].ID < sRules[j].ID })

	type location struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine   int `json:"startLine"`
				StartColumn int `json:"startColumn,omitempty"`
			} `json:"region"`
		} `json:"physicalLocation"`
	}
	type result struct {
		RuleID  string `json:"ruleId"`
		Level   string `json:"level"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Locations []location `json:"locations"`
	}

	results := []result{}
	for _, f := range findings {
		var res result
		res.RuleID = f.RuleID
		res.Level = levels[f.Severity]
		res.Message.Text = fmt.Sprintf("%s (%s) — %s", f.Title, f.Symbol, f.Remediation)
		var loc location
		loc.PhysicalLocation.ArtifactLocation.URI = f.File
		loc.PhysicalLocation.Region.StartLine = f.Line
		loc.PhysicalLocation.Region.StartColumn = f.Column
		res.Locations = []location{loc}
		results = append(results, res)
	}

	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool": map[string]any{
				"driver": map[string]any{
					"name":            "turion-platform-drift",
					"informationUri":  "https://turion-platform-drift",
					"semanticVersion": Version,
					"rules":           sRules,
				},
			},
			"results": results,
			"properties": map[string]any{
				"portabilityScore": sc.Score,
				"grade":            sc.Grade,
			},
		}},
	}
	return json.MarshalIndent(doc, "", "  ")
}

// HTML renders a standalone scorecard (inline CSS only, air-gap safe).
func HTML(sc *score.Scorecard, vr *validate.Report) (string, error) {
	var b strings.Builder
	gradeCol := map[score.Grade]string{
		score.GradeA: "#1f9d55", score.GradeB: "#2b7fd4",
		score.GradeC: "#c98a00", score.GradeD: "#d1651a", score.GradeF: "#d64545",
	}[sc.Grade]

	sevCol := map[rules.Severity]string{
		rules.SeverityCritical: "#d64545", rules.SeverityHigh: "#c98a00",
		rules.SeverityMedium: "#2b7fd4", rules.SeverityLow: "#8a94a6",
	}

	var catRows strings.Builder
	for _, cat := range sc.Categories {
		catRows.WriteString(fmt.Sprintf(
			`<tr><td>%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.0f</td></tr>`,
			html.EscapeString(string(cat.Category)), cat.Findings, cat.Critical, cat.Penalty))
	}

	var ruleRows strings.Builder
	for _, r := range sc.ByRule {
		ruleRows.WriteString(fmt.Sprintf(
			`<tr><td><code>%s</code></td><td><span class="sev" style="background:%s">%s</span></td>`+
				`<td class="num">%d</td><td>%s<div class="rem">→ %s</div></td></tr>`,
			r.RuleID, sevCol[r.Severity], string(r.Severity), r.Count,
			html.EscapeString(r.Title), html.EscapeString(r.Remediation)))
	}

	var hotRows strings.Builder
	for _, f := range sc.TopHotspots {
		hotRows.WriteString(fmt.Sprintf(
			`<tr><td><code>%s:%d</code></td><td><code>%s</code></td><td>%s<div class="rem">→ %s</div></td></tr>`,
			html.EscapeString(f.File), f.Line, f.RuleID,
			html.EscapeString(scan.Truncate(f.Symbol, 70)), html.EscapeString(f.Portability)))
	}

	validation := ""
	if vr != nil && vr.Objects > 0 {
		status := "PASS"
		color := "#1f9d55"
		if len(vr.Errors()) > 0 {
			status, color = "FAIL", "#d64545"
		}
		kubectl := "skipped — " + vr.KubectlSkip
		if vr.KubectlRan {
			kubectl = vr.KubectlCmd + " → " + map[bool]string{true: "PASS", false: "FAIL"}[vr.KubectlOK]
		}
		validation = fmt.Sprintf(`
  <section class="card">
    <h2>Air-gapped manifest validation</h2>
    <div class="kv"><span>Objects</span><b>%d across %d file(s)</b></div>
    <div class="kv"><span>Schema + structure</span><b style="color:%s">%s</b></div>
    <div class="kv"><span>kubectl dry-run</span><b>%s</b></div>
    <div class="kv"><span>Residual AWS references</span><b style="color:%s">%d</b></div>
  </section>`, vr.Objects, vr.Files, color, status,
			html.EscapeString(kubectl),
			map[bool]string{true: "#1f9d55", false: "#d64545"}[len(vr.ResidualAWS) == 0],
			len(vr.ResidualAWS))
	}

	generated := ""
	if len(sc.Generated) > 0 {
		var items strings.Builder
		for _, g := range sc.Generated {
			items.WriteString("<li><code>" + html.EscapeString(g) + "</code></li>")
		}
		generated = `<section class="card"><h2>Generated portable replacements</h2><ul>` +
			items.String() + "</ul></section>"
	}

	b.WriteString(fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>turion-platform-drift — portability scorecard</title>
<style>
  :root { color-scheme: light; }
  * { box-sizing: border-box; }
  body { margin:0; background:#f5f6f8; color:#14181f;
         font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif; }
  .wrap { max-width:1040px; margin:0 auto; padding:26px 20px 60px; }
  .hero { background:#0e1420; color:#e8edf5; border-radius:14px; padding:24px; display:flex;
          gap:26px; align-items:center; flex-wrap:wrap; }
  .hero h1 { margin:0; font-size:19px; font-weight:600; }
  .hero .sub { color:#8f9bb0; font-size:12.5px; margin-top:4px; }
  .score { margin-left:auto; text-align:center; }
  .score .n { font-size:42px; font-weight:750; line-height:1; color:%s; }
  .score .g { font-size:12px; letter-spacing:2px; color:#8f9bb0; margin-top:4px; }
  .verdict { color:#a9b4c6; font-size:13px; margin:14px 0 0; }
  .tiles { display:flex; gap:11px; flex-wrap:wrap; margin:20px 0; }
  .tile { background:#fff; border:1px solid #e3e6ec; border-radius:11px; padding:12px 16px; min-width:96px; }
  .tile .v { font-size:21px; font-weight:700; }
  .tile .l { font-size:11px; text-transform:uppercase; letter-spacing:.7px; color:#69727f; }
  .card { background:#fff; border:1px solid #e3e6ec; border-radius:12px; padding:16px 18px; margin-bottom:16px; }
  .card h2 { margin:0 0 12px; font-size:14.5px; }
  table { width:100%%; border-collapse:collapse; }
  th { text-align:left; font-size:11px; letter-spacing:.6px; text-transform:uppercase;
       color:#7b8593; padding:7px 8px; border-bottom:1px solid #eef0f4; }
  td { padding:8px; border-bottom:1px solid #f2f4f7; font-size:13px; vertical-align:top; }
  tr:last-child td { border-bottom:none; }
  .num { text-align:right; font-variant-numeric:tabular-nums; }
  code { background:#f1f3f6; padding:1px 5px; border-radius:4px; font-size:12px; }
  .sev { color:#fff; font-size:10px; font-weight:700; letter-spacing:.5px; padding:2px 7px; border-radius:999px; }
  .rem { color:#7b8593; font-size:12px; margin-top:2px; }
  .kv { display:flex; justify-content:space-between; padding:5px 0; border-bottom:1px solid #f2f4f7; font-size:13px; }
  .kv:last-child { border-bottom:none; }
  .kv span { color:#69727f; }
  ul { margin:0; padding-left:18px; font-size:13px; }
  footer { color:#8a94a6; font-size:11.5px; text-align:center; margin-top:24px; }
</style>
</head>
<body>
<div class="wrap">
  <div class="hero">
    <div>
      <h1>turion-platform-drift</h1>
      <div class="sub">Cloud-agnostic ground-station portability scorecard &middot; %s</div>
      <div class="verdict">%s</div>
    </div>
    <div class="score">
      <div class="n">%d</div>
      <div class="g">GRADE %s</div>
    </div>
  </div>

  <div class="tiles">
    <div class="tile"><div class="v">%d</div><div class="l">Findings</div></div>
    <div class="tile"><div class="v" style="color:#d64545">%d</div><div class="l">Critical</div></div>
    <div class="tile"><div class="v" style="color:#c98a00">%d</div><div class="l">High</div></div>
    <div class="tile"><div class="v">%d</div><div class="l">Files</div></div>
    <div class="tile"><div class="v">%.1fs</div><div class="l">Scan time</div></div>
  </div>

  <section class="card">
    <h2>Coupling by capability</h2>
    <table><thead><tr><th>Capability</th><th class="num">Findings</th><th class="num">Critical</th><th class="num">Penalty</th></tr></thead>
    <tbody>%s</tbody></table>
  </section>

  <section class="card">
    <h2>Rules triggered</h2>
    <table><thead><tr><th>Rule</th><th>Severity</th><th class="num">Count</th><th>Finding &amp; portable replacement</th></tr></thead>
    <tbody>%s</tbody></table>
  </section>

  <section class="card">
    <h2>Top hotspots</h2>
    <table><thead><tr><th>Location</th><th>Rule</th><th>Symbol &amp; replacement</th></tr></thead>
    <tbody>%s</tbody></table>
  </section>
%s
%s
  <footer>turion-platform-drift 1.0.0 &middot; no proprietary AWS API called during analysis</footer>
</div>
</body>
</html>
`, gradeCol, html.EscapeString(sc.Root),
		html.EscapeString(sc.Verdict),
		sc.Score, string(sc.Grade),
		sc.TotalFindings, sc.CriticalCount, sc.HighCount, sc.FilesScanned, sc.DurationSec,
		catRows.String(), ruleRows.String(), hotRows.String(),
		validation, generated))
	return b.String(), nil
}

// WriteHTML writes the HTML report.
func WriteHTML(path string, sc *score.Scorecard, vr *validate.Report) error {
	doc, err := HTML(sc, vr)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(doc), 0o644)
}

// Now is a formatted timestamp used by generated headers.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }
