// Package score turns a scan result into a portability scorecard.
package score

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
)

// Grade is a letter band for the portability score.
type Grade string

const (
	GradeA Grade = "A" // cloud-agnostic
	GradeB Grade = "B" // portable with minor work
	GradeC Grade = "C" // significant coupling
	GradeD Grade = "D" // heavily AWS-bound
	GradeF Grade = "F" // cannot run off-AWS
)

// CategoryScore summarises one capability area.
type CategoryScore struct {
	Category rules.Category `json:"category"`
	Findings int            `json:"findings"`
	Critical int            `json:"critical"`
	Penalty  float64        `json:"penalty"`
}

// Scorecard is the deliverable printed by `scan`.
type Scorecard struct {
	Root          string          `json:"root"`
	Score         int             `json:"score"`
	Grade         Grade           `json:"grade"`
	Verdict       string          `json:"verdict"`
	FilesScanned  int             `json:"files_scanned"`
	TotalFindings int             `json:"total_findings"`
	CriticalCount int             `json:"critical"`
	HighCount     int             `json:"high"`
	MediumCount   int             `json:"medium"`
	LowCount      int             `json:"low"`
	DistinctRules int             `json:"distinct_rules"`
	Categories    []CategoryScore `json:"categories"`
	ByRule        []RuleBucket    `json:"by_rule"`
	Languages     map[string]int  `json:"languages"`
	DurationSec   float64         `json:"duration_seconds"`
	PythonAST     bool            `json:"python_ast"`
	TopHotspots   []scan.Finding  `json:"top_hotspots"`
	Errors        []string        `json:"errors,omitempty"`
	// Generated lists artifacts the generator produced (filled by the caller).
	Generated []string `json:"generated,omitempty"`
}

// RuleBucket is a per-rule count with its remediation.
type RuleBucket struct {
	RuleID      string         `json:"rule_id"`
	Title       string         `json:"title"`
	Severity    rules.Severity `json:"severity"`
	Category    rules.Category `json:"category"`
	Count       int            `json:"count"`
	Remediation string         `json:"remediation"`
	Portability string         `json:"portability"`
}

// Build computes a scorecard from a scan result.
func Build(res *scan.Result) *Scorecard {
	sc := &Scorecard{
		Root:          res.Root,
		FilesScanned:  res.Files.Total,
		TotalFindings: len(res.Findings),
		Languages:     res.Files.ByLang,
		DurationSec:   res.Duration,
		PythonAST:     res.PythonAST,
		Errors:        res.Errors,
		DistinctRules: len(res.ByRule),
	}

	severityWeight := map[rules.Severity]float64{
		rules.SeverityCritical: 12,
		rules.SeverityHigh:     7,
		rules.SeverityMedium:   3,
		rules.SeverityLow:      1,
	}

	catPenalty := map[rules.Category]float64{}
	catFindings := map[rules.Category]int{}
	catCritical := map[rules.Category]int{}
	penalty := 0.0

	for _, f := range res.Findings {
		r, ok := rules.Get(f.RuleID)
		sev := f.Severity
		if ok {
			sev = r.Severity
		}
		w := severityWeight[sev]
		penalty += w
		catPenalty[f.Category] += w
		catFindings[f.Category]++
		switch f.Severity {
		case rules.SeverityCritical:
			sc.CriticalCount++
			catCritical[f.Category]++
		case rules.SeverityHigh:
			sc.HighCount++
		case rules.SeverityMedium:
			sc.MediumCount++
		default:
			sc.LowCount++
		}
	}

	// Diminishing returns: many findings of the same kind should not push the
	// score below zero, but a single critical coupling should still dominate.
	penalty = 100 * (1 - math.Exp(-penalty/45))
	score := int(math.Round(100 - penalty))
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	sc.Score = score
	sc.Grade = gradeFor(score)
	sc.Verdict = verdictFor(sc.Grade, sc)

	cats := make([]CategoryScore, 0, len(catFindings))
	for cat, n := range catFindings {
		cats = append(cats, CategoryScore{
			Category: cat, Findings: n, Critical: catCritical[cat], Penalty: catPenalty[cat],
		})
	}
	sort.Slice(cats, func(i, j int) bool {
		if cats[i].Penalty != cats[j].Penalty {
			return cats[i].Penalty > cats[j].Penalty
		}
		return cats[i].Category < cats[j].Category
	})
	sc.Categories = cats

	for id, n := range res.ByRule {
		r, _ := rules.Get(id)
		sc.ByRule = append(sc.ByRule, RuleBucket{
			RuleID: id, Title: r.Title, Severity: r.Severity, Category: r.Category,
			Count: n, Remediation: r.Remediation, Portability: r.Portability,
		})
	}
	sort.Slice(sc.ByRule, func(i, j int) bool {
		si, sj := severityRank(sc.ByRule[i].Severity), severityRank(sc.ByRule[j].Severity)
		if si != sj {
			return si < sj
		}
		return sc.ByRule[i].RuleID < sc.ByRule[j].RuleID
	})

	// Hotspots: critical findings first, then by file.
	hot := append([]scan.Finding(nil), res.Findings...)
	sort.SliceStable(hot, func(i, j int) bool {
		si, sj := severityRank(hot[i].Severity), severityRank(hot[j].Severity)
		if si != sj {
			return si < sj
		}
		if hot[i].File != hot[j].File {
			return hot[i].File < hot[j].File
		}
		return hot[i].Line < hot[j].Line
	})
	if len(hot) > 12 {
		hot = hot[:12]
	}
	sc.TopHotspots = hot

	return sc
}

func severityRank(s rules.Severity) int {
	switch s {
	case rules.SeverityCritical:
		return 0
	case rules.SeverityHigh:
		return 1
	case rules.SeverityMedium:
		return 2
	default:
		return 3
	}
}

func gradeFor(score int) Grade {
	switch {
	case score >= 90:
		return GradeA
	case score >= 75:
		return GradeB
	case score >= 55:
		return GradeC
	case score >= 30:
		return GradeD
	default:
		return GradeF
	}
}

func verdictFor(g Grade, sc *Scorecard) string {
	switch g {
	case GradeA:
		return "Cloud-agnostic: deployable to a hybrid, air-gapped, or on-premises ground station as-is."
	case GradeB:
		return "Portable with minor work: a handful of couplings to externalise into configuration."
	case GradeC:
		return "Significant coupling: several ground services require an AWS control plane today."
	case GradeD:
		return "Heavily AWS-bound: most ground-segment ETL and telemetry paths need re-hosting."
	default:
		return fmt.Sprintf(
			"Cannot run off-AWS: %d critical couplings block an air-gapped deployment.",
			sc.CriticalCount)
	}
}

// Gate is a CI threshold check.
type Gate struct {
	MaxScore       int  // fail if score < MaxScore
	FailOnCritical bool // fail if any critical finding exists
	FailOnAny      bool // fail if any finding exists
}

// Enforce returns a list of gate violations (empty means pass).
func (s *Scorecard) Enforce(g Gate) []string {
	var problems []string
	if g.MaxScore > 0 && s.Score < g.MaxScore {
		problems = append(problems, fmt.Sprintf(
			"portability score %d is below the required minimum %d", s.Score, g.MaxScore))
	}
	if g.FailOnCritical && s.CriticalCount > 0 {
		problems = append(problems, fmt.Sprintf(
			"%d critical AWS coupling(s) block an air-gapped deployment", s.CriticalCount))
	}
	if g.FailOnAny && s.TotalFindings > 0 {
		problems = append(problems, fmt.Sprintf(
			"%d AWS coupling(s) found; this gate requires zero", s.TotalFindings))
	}
	return problems
}

// Bar renders a text progress bar for the score.
func Bar(score, width int) string {
	if width <= 0 {
		width = 32
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	filled := int(math.Round(float64(score) / 100 * float64(width)))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
