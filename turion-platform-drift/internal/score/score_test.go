package score

import (
	"testing"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
)

func mk(rules map[string]int) *scan.Result {
	res := &scan.Result{ByRule: map[string]int{}, Files: scan.FileStats{Total: 10}}
	for id, n := range rules {
		r, _ := rules2Get(id)
		for i := 0; i < n; i++ {
			res.Findings = append(res.Findings, scan.Finding{
				RuleID: id, Severity: r.Severity, Category: r.Category,
				File: "x.py", Line: i + 1,
			})
		}
	}
	return res
}

func rules2Get(id string) (rules.Rule, bool) { return rules.Get(id) }

func TestCleanRepoIsGradeA(t *testing.T) {
	sc := Build(&scan.Result{ByRule: map[string]int{}, Files: scan.FileStats{Total: 20}})
	if sc.Score != 100 || sc.Grade != GradeA {
		t.Fatalf("clean repo scored %d (%s), want 100 (A)", sc.Score, sc.Grade)
	}
	if sc.Verdict == "" {
		t.Error("verdict should be populated")
	}
}

func TestHeavilyCoupledRepoIsGradeF(t *testing.T) {
	sc := Build(mk(map[string]int{
		"TPD001": 8, "TPD003": 5, "TPD004": 6, "TPD007": 9, "TPD009": 4,
		"TPD020": 3, "TPD040": 2, "TPD043": 2,
	}))
	if sc.Grade != GradeF {
		t.Fatalf("grade = %s (score %d), want F", sc.Grade, sc.Score)
	}
	if sc.CriticalCount == 0 {
		t.Error("critical count should be non-zero")
	}
}

func TestMonotonicScoring(t *testing.T) {
	prev := 101
	for _, n := range []int{0, 1, 2, 5, 10, 25} {
		sc := Build(mk(map[string]int{"TPD001": n}))
		if n > 0 && sc.Score > prev {
			t.Fatalf("score increased with more findings: %d -> %d", prev, sc.Score)
		}
		prev = sc.Score
	}
}

func TestCategoriesSortedByPenalty(t *testing.T) {
	sc := Build(mk(map[string]int{"TPD001": 5, "TPD044": 2}))
	if len(sc.Categories) < 2 {
		t.Fatalf("expected >= 2 categories, got %d", len(sc.Categories))
	}
	if sc.Categories[0].Penalty < sc.Categories[1].Penalty {
		t.Error("categories should be ordered by descending penalty")
	}
}

func TestTopHotspotsCapped(t *testing.T) {
	sc := Build(mk(map[string]int{"TPD001": 50}))
	if len(sc.TopHotspots) > 12 {
		t.Fatalf("hotspots = %d, want <= 12", len(sc.TopHotspots))
	}
}

func TestGate(t *testing.T) {
	clean := Build(&scan.Result{ByRule: map[string]int{}, Files: scan.FileStats{Total: 1}})
	if p := clean.Enforce(Gate{MaxScore: 90, FailOnCritical: true}); len(p) != 0 {
		t.Fatalf("clean repo failed its own gate: %v", p)
	}
	dirty := Build(mk(map[string]int{"TPD001": 3}))
	if p := dirty.Enforce(Gate{MaxScore: 90}); len(p) == 0 {
		t.Error("gate should fail on a low score")
	}
	if p := dirty.Enforce(Gate{FailOnCritical: true}); len(p) == 0 {
		t.Error("gate should fail on critical findings")
	}
	if p := dirty.Enforce(Gate{FailOnAny: true}); len(p) == 0 {
		t.Error("gate should fail on any finding")
	}
	if p := dirty.Enforce(Gate{}); len(p) != 0 {
		t.Errorf("an empty gate should pass, got %v", p)
	}
}

func TestBarIsClamped(t *testing.T) {
	for _, n := range []int{-50, 0, 50, 100, 400} {
		b := Bar(n, 20)
		if len([]rune(b)) != 20 {
			t.Fatalf("Bar(%d) rendered %d cells, want 20", n, len([]rune(b)))
		}
	}
}

func TestGradeForBoundaries(t *testing.T) {
	cases := map[int]Grade{100: GradeA, 90: GradeA, 89: GradeB, 75: GradeB,
		74: GradeC, 55: GradeC, 54: GradeD, 30: GradeD, 29: GradeF, 0: GradeF}
	for score, want := range cases {
		if got := gradeFor(score); got != want {
			t.Errorf("gradeFor(%d) = %s, want %s", score, got, want)
		}
	}
}
