package scan

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed analyzer.py
var pythonAnalyzer string

// Options controls a scan.
type Options struct {
	// Root is the repository to analyse.
	Root string
	// Exclude is a list of path substrings to skip.
	Exclude []string
	// NoPython disables the Python AST analyzer and falls back to the Go
	// heuristic reader for .py files.
	NoPython bool
	// MaxFileBytes skips files larger than this (default 2 MiB).
	MaxFileBytes int64
	// PythonBin is the interpreter used for the Python AST pass.
	PythonBin string
}

// Result is the outcome of a full scan.
type Result struct {
	Root       string         `json:"root"`
	StartedAt  time.Time      `json:"started_at"`
	Duration   float64        `json:"duration_seconds"`
	Files      FileStats      `json:"files"`
	Findings   []Finding      `json:"findings"`
	ByRule     map[string]int `json:"findings_by_rule"`
	ByCategory map[string]int `json:"findings_by_category"`
	Errors     []string       `json:"errors"`
	PythonAST  bool           `json:"python_ast_used"`
}

// FileStats counts what was analysed.
type FileStats struct {
	Total  int            `json:"total"`
	ByLang map[string]int `json:"by_language"`
}

var defaultExcludes = []string{
	"/.git/", "/node_modules/", "/vendor/", "/dist/", "/build/",
	"/.venv/", "/venv/", "/__pycache__/", "/.mypy_cache/", "/.terraform/",
	"/artifacts/", "/out/", "/target/", "/.idea/", "/.vscode/",
}

// langFor classifies a file by extension, then by well-known basename.
func langFor(path string) (Language, bool) {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "makefile", "gnumakefile", "dockerfile", "containerfile", "jenkinsfile":
		return LangShell, true
	}
	if strings.HasSuffix(base, ".dockerfile") {
		return LangShell, true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return LangGo, true
	case ".py", ".pyi":
		return LangPython, true
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte":
		return LangTypeScript, true
	case ".yaml", ".yml", ".tpl":
		return LangYAML, true
	case ".json":
		return LangJSON, true
	case ".tf", ".tfvars", ".hcl", ".nomad":
		return LangHCL, true
	case ".jsonnet", ".libsonnet":
		return LangJSONNet, true
	case ".sh", ".bash", ".zsh", ".mk", ".mkfile":
		return LangShell, true
	}
	return "", false
}

// Run walks opts.Root and analyses every supported file.
func Run(opts Options) (*Result, error) {
	started := time.Now()
	if opts.MaxFileBytes == 0 {
		opts.MaxFileBytes = 2 << 20
	}
	if opts.PythonBin == "" {
		opts.PythonBin = "python3"
	}
	info, err := os.Stat(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", opts.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", opts.Root)
	}

	excludes := append(append([]string{}, defaultExcludes...), opts.Exclude...)
	col := NewCollector()
	stats := FileStats{ByLang: map[string]int{}}

	var goFiles, pyFiles []string
	type pending struct {
		rel  string
		lang Language
	}
	var textFiles []pending

	walkErr := filepath.Walk(opts.Root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			col.AddError("%s: %v", path, err)
			return nil
		}
		if fi.IsDir() {
			if path != opts.Root && excluded(path, excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		if excluded(path, excludes) || fi.Size() > opts.MaxFileBytes {
			return nil
		}
		rel, relErr := filepath.Rel(opts.Root, path)
		if relErr != nil {
			return nil
		}
		lang, ok := langFor(path)
		if !ok {
			return nil
		}
		stats.Total++
		stats.ByLang[string(lang)]++
		switch lang {
		case LangGo:
			goFiles = append(goFiles, rel)
		case LangPython:
			pyFiles = append(pyFiles, rel)
		default:
			textFiles = append(textFiles, pending{rel: rel, lang: lang})
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// ---- Go: standard-library AST ----
	for _, rel := range goFiles {
		ScanGo(opts.Root, rel, col)
	}

	// ---- Python: real AST via the embedded analyzer ----
	pythonUsed := false
	if len(pyFiles) > 0 {
		if !opts.NoPython {
			if findings, errs, ok := runPythonAST(opts, pyFiles); ok {
				pythonUsed = true
				for _, f := range findings {
					col.Add(f)
				}
				for _, e := range errs {
					col.AddError("%s", e)
				}
			}
		}
		if !pythonUsed {
			for _, rel := range pyFiles {
				content, err := ReadFile(filepath.Join(opts.Root, rel))
				if err != nil {
					col.AddError("%s: %v", rel, err)
					continue
				}
				scanPythonHeuristic(rel, content, col)
			}
		}
	}

	// ---- TypeScript / YAML / JSON / HCL / shell ----
	for _, p := range textFiles {
		content, err := ReadFile(filepath.Join(opts.Root, p.rel))
		if err != nil {
			col.AddError("%s: %v", p.rel, err)
			continue
		}
		switch p.lang {
		case LangYAML:
			ScanText(p.rel, content, p.lang, col)
			ScanManifest(p.rel, content, p.lang, col)
			if strings.Contains(p.rel, "batch") || strings.Contains(strings.ToLower(content), "jobdefinitionname") {
				ScanAWSBatchYAML(p.rel, content, col)
			}
		case LangJSON:
			ScanText(p.rel, content, p.lang, col)
			ScanManifest(p.rel, content, p.lang, col)
		default:
			ScanText(p.rel, content, p.lang, col)
		}
	}

	findings := col.Findings()
	res := &Result{
		Root:       opts.Root,
		StartedAt:  started,
		Duration:   time.Since(started).Seconds(),
		Files:      stats,
		Findings:   findings,
		ByRule:     map[string]int{},
		ByCategory: map[string]int{},
		Errors:     col.Errors(),
		PythonAST:  pythonUsed,
	}
	for _, f := range findings {
		res.ByRule[f.RuleID]++
		res.ByCategory[string(f.Category)]++
	}
	return res, nil
}

func excluded(path string, excludes []string) bool {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	for _, ex := range excludes {
		if ex == "" {
			continue
		}
		if strings.Contains(p, strings.ReplaceAll(ex, "\\", "/")) {
			return true
		}
	}
	return false
}

// pythonASTRequest is the JSON contract with analyzer.py.
type pythonASTRequest struct {
	Root  string   `json:"root"`
	Files []string `json:"files"`
}

type pythonASTFinding struct {
	File       string  `json:"file"`
	RuleID     string  `json:"rule_id"`
	Line       int     `json:"line"`
	Column     int     `json:"column"`
	Symbol     string  `json:"symbol"`
	Service    string  `json:"service"`
	Message    string  `json:"message"`
	Confidence float64 `json:"confidence"`
}

type pythonASTResponse struct {
	Findings []pythonASTFinding `json:"findings"`
	Errors   []string           `json:"errors"`
}

// runPythonAST executes the embedded analyzer and converts its output.
func runPythonAST(opts Options, files []string) ([]Finding, []string, bool) {
	dir, err := os.MkdirTemp("", "tpd-pyast-*")
	if err != nil {
		return nil, []string{fmt.Sprintf("python ast: %v", err)}, false
	}
	defer os.RemoveAll(dir)

	script := filepath.Join(dir, "analyzer.py")
	if err := os.WriteFile(script, []byte(pythonAnalyzer), 0o600); err != nil {
		return nil, []string{fmt.Sprintf("python ast: %v", err)}, false
	}

	req := pythonASTRequest{Root: opts.Root, Files: files}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, []string{fmt.Sprintf("python ast: %v", err)}, false
	}

	cmd := exec.Command(opts.PythonBin, script)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Dir = opts.Root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, []string{fmt.Sprintf(
			"python ast analyzer unavailable (%v); falling back to heuristic .py scan", err)}, false
	}

	var resp pythonASTResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, []string{fmt.Sprintf("python ast: bad analyzer output: %v", err)}, false
	}

	findings := make([]Finding, 0, len(resp.Findings))
	for _, f := range resp.Findings {
		nf := New(f.RuleID, f.File, f.Line, LangPython, f.Symbol,
			strings.Trim(f.Service, "<>"), f.Message)
		nf.Column = f.Column
		if f.Confidence > 0 {
			nf.Confidence = f.Confidence
		}
		findings = append(findings, nf)
	}
	return findings, resp.Errors, true
}

// AnalyzePythonSource runs the embedded Python AST analyzer over source held in
// memory. Exported for fuzzing; returns ok=false when no interpreter is usable.
func AnalyzePythonSource(rel, src string) ([]Finding, []string, bool) {
	dir, err := os.MkdirTemp("", "tpd-pyast-src-*")
	if err != nil {
		return nil, []string{err.Error()}, false
	}
	defer os.RemoveAll(dir)
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, []string{err.Error()}, false
	}
	if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
		return nil, []string{err.Error()}, false
	}
	bin := "python3"
	if p, err := exec.LookPath(bin); err == nil {
		bin = p
	}
	return runPythonAST(Options{Root: dir, PythonBin: bin}, []string{rel})
}

// scanPythonHeuristic is the no-interpreter fallback: line-based, conservative.
func scanPythonHeuristic(rel, content string, c *Collector) {
	ScanText(rel, content, LangPython, c)
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "import boto3"), strings.HasPrefix(lower, "import botocore"),
			strings.HasPrefix(lower, "from boto3"), strings.HasPrefix(lower, "from botocore"),
			strings.HasPrefix(lower, "import aioboto3"), strings.HasPrefix(lower, "from aioboto3"):
			c.Add(New("TPD001", rel, i+1, LangPython, Truncate(trimmed, 70), "boto3",
				"imports the proprietary AWS SDK"))
		case strings.Contains(lower, "boto3.client(") || strings.Contains(lower, "boto3.resource("):
			c.Add(New("TPD002", rel, i+1, LangPython, Truncate(trimmed, 70), "boto3",
				"constructs a boto3 client"))
		case strings.Contains(lower, ".invoke(") && strings.Contains(lower, "lambda"):
			c.Add(New("TPD003", rel, i+1, LangPython, Truncate(trimmed, 70), "lambda",
				"invokes an AWS Lambda function"))
		case strings.Contains(lower, "submit_job("):
			c.Add(New("TPD004", rel, i+1, LangPython, Truncate(trimmed, 70), "batch",
				"submits work to AWS Batch"))
		case strings.Contains(lower, "assume_role("):
			c.Add(New("TPD008", rel, i+1, LangPython, Truncate(trimmed, 70), "sts",
				"assumes an AWS IAM role"))
		}
	}
}

// RuleIDs returns the distinct rule IDs present, sorted.
func (r *Result) RuleIDs() []string {
	ids := make([]string, 0, len(r.ByRule))
	for id := range r.ByRule {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
