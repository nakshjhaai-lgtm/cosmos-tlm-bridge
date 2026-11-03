// Package scan walks a ground-service repository and produces portability
// findings from Go ASTs, Python ASTs, TypeScript sources, and Kubernetes/Helm
// manifests.
package scan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"turion-platform-drift/internal/rules"
)

// Language identifies how a file was analysed.
type Language string

const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangTypeScript Language = "typescript"
	LangYAML       Language = "yaml"
	LangJSON       Language = "json"
	LangHCL        Language = "hcl"
	LangJSONNet    Language = "jsonnet"
	LangShell      Language = "shell"
)

// Finding is one detected AWS dependency.
type Finding struct {
	RuleID      string         `json:"rule_id"`
	Title       string         `json:"title"`
	Category    rules.Category `json:"category"`
	Severity    rules.Severity `json:"severity"`
	File        string         `json:"file"`
	Line        int            `json:"line"`
	Column      int            `json:"column,omitempty"`
	Language    Language       `json:"language"`
	Snippet     string         `json:"snippet,omitempty"`
	Symbol      string         `json:"symbol,omitempty"`
	Message     string         `json:"message"`
	Remediation string         `json:"remediation"`
	Portability string         `json:"portability"`
	// Serviced names the AWS service, for the generator and the report.
	Service string `json:"service,omitempty"`
	// Confidence is 1.0 for AST-derived findings, lower for heuristics.
	Confidence float64 `json:"confidence"`
}

// Key uniquely identifies a finding within a repository.
func (f Finding) Key() string {
	return fmt.Sprintf("%s|%s|%d|%s", f.RuleID, f.File, f.Line, f.Symbol)
}

// New builds a Finding from a rule plus location context.
func New(ruleID, file string, line int, lang Language, symbol, service, message string) Finding {
	r, ok := rules.Get(ruleID)
	if !ok {
		r = rules.Rule{ID: ruleID, Title: ruleID, Severity: rules.SeverityMedium}
	}
	if message == "" {
		message = r.Description
	}
	return Finding{
		RuleID:      r.ID,
		Title:       r.Title,
		Category:    r.Category,
		Severity:    r.Severity,
		File:        filepath.ToSlash(file),
		Line:        line,
		Language:    lang,
		Symbol:      symbol,
		Message:     message,
		Remediation: r.Remediation,
		Portability: r.Portability,
		Service:     service,
		Confidence:  1.0,
	}
}

// Collector accumulates findings, de-duplicating and keeping order stable.
type Collector struct {
	findings []Finding
	seen     map[string]struct{}
	// atSite maps rule|file|line to the index of the finding already recorded
	// there, so two scanners reporting the same dependency on the same line do
	// not inflate the score.
	atSite map[string]int
	errs   []string
}

// NewCollector returns an empty collector.
func NewCollector() *Collector {
	return &Collector{seen: map[string]struct{}{}, atSite: map[string]int{}}
}

// siteKey identifies one rule fired at one source location.
func siteKey(ruleID, file string, line int) string {
	return fmt.Sprintf("%s|%s|%d", ruleID, file, line)
}

// Add records a finding unless it duplicates one already collected. Two
// findings for the same rule on the same line are treated as one dependency
// when their symbols overlap (e.g. "jobQueueName: q" and "jobQueueName"); the
// more specific symbol wins.
func (c *Collector) Add(f Finding) {
	k := f.Key()
	if _, dup := c.seen[k]; dup {
		return
	}
	sk := siteKey(f.RuleID, f.File, f.Line)
	if i, ok := c.atSite[sk]; ok {
		prev := c.findings[i]
		if symbolsOverlap(prev.Symbol, f.Symbol) {
			if len(strings.TrimSpace(f.Symbol)) > len(strings.TrimSpace(prev.Symbol)) {
				delete(c.seen, prev.Key())
				c.findings[i] = f
				c.seen[k] = struct{}{}
			}
			return
		}
	}
	c.seen[k] = struct{}{}
	c.atSite[sk] = len(c.findings)
	c.findings = append(c.findings, f)
}

// symbolsOverlap reports whether two symbols describe the same source span.
func symbolsOverlap(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return true
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// AddError records a non-fatal scan problem (unreadable file, parse failure).
func (c *Collector) AddError(format string, args ...any) {
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
}

// Findings returns findings sorted by file then line.
func (c *Collector) Findings() []Finding {
	out := append([]Finding(nil), c.findings...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].RuleID < out[j].RuleID
	})
	return out
}

// Errors returns scan problems.
func (c *Collector) Errors() []string { return c.errs }

// Merge folds another collector into this one.
func (c *Collector) Merge(other *Collector) {
	if other == nil {
		return
	}
	for _, f := range other.findings {
		c.Add(f)
	}
	c.errs = append(c.errs, other.errs...)
}

// Truncate shortens a line for display.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
