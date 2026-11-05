// Command turion-platform-drift is a cloud-agnostic ground-station portability
// linter for Turion Space ground-segment codebases.
//
// It statically analyses Go, Python, and TypeScript sources plus
// Kubernetes/Helm/AWS Batch manifests for hard-coded AWS lock-in, scores the
// repository's portability, and generates portable Kubernetes/Crossplane/
// OpenTelemetry replacements.
//
//	turion-platform-drift scan ./ground-service
//	turion-platform-drift scan ./ground-service --generate --out ./generated
//	turion-platform-drift validate ./generated
//	turion-platform-drift rules
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"turion-platform-drift/internal/generate"
	"turion-platform-drift/internal/report"
	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
	"turion-platform-drift/internal/score"
	"turion-platform-drift/internal/validate"
)

const version = "1.0.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage() {
	fmt.Print(`turion-platform-drift ` + version + ` — cloud-agnostic ground-station portability linter

USAGE
  turion-platform-drift <command> [flags]

COMMANDS
  scan <path>        Analyse a repository and print the portability scorecard
  generate <path>    Scan, then emit portable replacement manifests
  validate <dir>     Validate generated manifests (air-gapped, no cluster needed)
  rules              List every detection rule and its portable replacement
  version            Print the version

COMMON FLAGS
  --format text|json|sarif   Output format for scan (default text)
  --out <dir>                Directory for reports and generated manifests
  --generate                 Generate replacements as part of scan
  --target kubernetes|crossplane
  --namespace <ns>           Target namespace (default ground-system)
  --object-store versitygw|minio
  --registry <host/path>     Ground-station registry (default registry.ground.local)
  --split                    One file per generated object instead of a bundle
  --exclude <substr>         Additional path exclusion (repeatable)
  --no-python                Skip the Python AST pass (regex fallback)
  --top <n>                  Hotspots to print (default 8)
  --fail-under <score>       Exit 1 when the score is below this value
  --fail-on-critical         Exit 1 when any critical coupling is found
  --fail-on-any              Exit 1 when any coupling is found
  --fail-on-residual-aws     Exit 1 when generated manifests still reference AWS

EXAMPLES
  go run turion-platform-drift scan ./ground-service
  go run turion-platform-drift scan ./ground-service --generate --out ./generated
  go run turion-platform-drift validate ./generated
  turion-platform-drift scan ./ground-service --format sarif --out ./reports
`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println("turion-platform-drift " + version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	case "rules":
		return cmdRules(args[1:])
	case "scan":
		return cmdScan(args[1:], false)
	case "generate":
		return cmdScan(args[1:], true)
	case "validate":
		return cmdValidate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func cmdRules(args []string) int {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	all := rules.All()
	// Group by capability so each heading is printed once.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Category != all[j].Category {
			return all[i].Category < all[j].Category
		}
		return all[i].ID < all[j].ID
	})
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
		return 0
	}
	fmt.Printf("\n  %d detection rules — turion-platform-drift %s\n", len(all), version)
	fmt.Println("  ─────────────────────────────────────────────────────────────────────────")
	lastCat := rules.Category("")
	for _, r := range all {
		if r.Category != lastCat {
			fmt.Printf("\n  %s\n", strings.ToUpper(string(r.Category)))
			lastCat = r.Category
		}
		fmt.Printf("    %s  %-8s  %s\n", r.ID, r.Severity, r.Title)
		fmt.Printf("        replaces:    %s\n", r.Portability)
		fmt.Printf("        remediation: %s\n", r.Remediation)
	}
	fmt.Println()
	return 0
}

type flags struct {
	format, outDir, target, namespace, objectStore, registry string
	split, noPython, generateReports                         bool
	excludes                                                 multiFlag
	top, failUnder                                           int
	failOnCritical, failOnAny, failOnResidualAWS             bool
	pythonBin, kubectl, kubeconfig                           string
	requireKubectl, serverDryRun, helmRender, requireHelm    bool
}

// boolFlags names flags that take no value, so argument reordering can tell
// "--generate" (no value) from "--out <dir>" (value follows).
var boolFlags = map[string]bool{
	"generate": true, "split": true, "no-python": true,
	"fail-on-critical": true, "fail-on-any": true, "fail-on-residual-aws": true,
	"json": true,
}

// reorderArgs lets flags appear before or after the positional path, which
// Go's flag package does not allow on its own.
func reorderArgs(args []string) ([]string, []string) {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || boolFlags[name] {
			continue
		}
		// A value flag: take the next token as its argument.
		if i+1 < len(args) {
			flagArgs = append(flagArgs, args[i+1])
			i++
		}
	}
	return flagArgs, positional
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func parseFlags(name string, args []string, gen bool) (*flags, string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	f := &flags{}
	fs.StringVar(&f.format, "format", "text", "text|json|sarif")
	fs.StringVar(&f.outDir, "out", "", "output directory for reports/manifests")
	fs.StringVar(&f.target, "target", "kubernetes", "kubernetes|crossplane")
	fs.StringVar(&f.namespace, "namespace", "ground-system", "target namespace")
	fs.StringVar(&f.objectStore, "object-store", "versitygw", "versitygw|minio")
	fs.StringVar(&f.registry, "registry", "registry.ground.local", "ground-station registry")
	fs.BoolVar(&f.split, "split", false, "one file per generated object")
	fs.BoolVar(&f.noPython, "no-python", false, "skip the Python AST pass")
	fs.IntVar(&f.top, "top", 8, "hotspots to print")
	fs.IntVar(&f.failUnder, "fail-under", 0, "exit 1 when score < N")
	fs.BoolVar(&f.failOnCritical, "fail-on-critical", false, "exit 1 on any critical finding")
	fs.BoolVar(&f.failOnAny, "fail-on-any", false, "exit 1 on any finding")
	fs.BoolVar(&f.failOnResidualAWS, "fail-on-residual-aws", false, "exit 1 if generated manifests reference AWS")
	fs.Var(&f.excludes, "exclude", "path substring to exclude (repeatable)")
	fs.StringVar(&f.pythonBin, "python", "python3", "python interpreter for the AST pass")
	fs.StringVar(&f.kubectl, "kubectl", "", "kubectl binary for the dry-run gate")
	fs.StringVar(&f.kubeconfig, "kubeconfig", "", "kubeconfig for the dry-run gate")
	fs.BoolVar(&f.requireKubectl, "require-kubectl", false, "fail if the kubectl dry-run gate cannot run")
	fs.BoolVar(&f.serverDryRun, "server-dry-run", false, "also run kubectl apply --dry-run=server")
	fs.BoolVar(&f.helmRender, "helm", false, "render Helm charts with helm template before validating")
	fs.BoolVar(&f.requireHelm, "require-helm", false, "fail if Helm rendering is requested but helm is missing")
	fs.BoolVar(&f.generateReports, "generate", false, "generate portable replacements")
	flagArgs, positional := reorderArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		return nil, "", err
	}
	root := ""
	if len(positional) > 0 {
		root = positional[0]
	}
	if root == "" {
		return nil, "", fmt.Errorf("scan needs a path, e.g. `turion-platform-drift scan ./ground-service`")
	}
	if f.generateReports || gen {
		f.generateReports = true
	}
	return f, root, nil
}

func cmdScan(args []string, forceGenerate bool) int {
	f, root, err := parseFlags("scan", args, forceGenerate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	res, err := scan.Run(scan.Options{
		Root:      root,
		Exclude:   f.excludes,
		NoPython:  f.noPython,
		PythonBin: f.pythonBin,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
		return 3
	}

	sc := score.Build(res)
	var vr *validate.Report

	if f.generateReports {
		outDir := f.outDir
		if outDir == "" {
			outDir = filepath.Join("generated", filepath.Base(absOr(root)))
		}
		gen, genErr := generate.Build(res, generate.Options{
			OutDir:      outDir,
			Namespace:   f.namespace,
			Target:      f.target,
			Registry:    f.registry,
			ObjectStore: f.objectStore,
			Split:       f.split,
		})
		if genErr != nil {
			fmt.Fprintf(os.Stderr, "generation failed: %v\n", genErr)
			return 4
		}
		sc.Generated = gen.Files

		vr, err = validate.DirOpts(outDir, validate.Options{
			Kubectl: f.kubectl, Kubeconfig: f.kubeconfig, RequireKubectl: f.requireKubectl,
			ServerDryRun: f.serverDryRun, Helm: true, RequireHelm: f.requireHelm,
			Namespace: f.namespace,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
			return 5
		}
	}

	// Reports.
	if f.outDir != "" {
		if err := os.MkdirAll(f.outDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", f.outDir, err)
			return 6
		}
		jsonDoc, _ := report.JSON(sc, vr, res.Findings)
		if err := os.WriteFile(filepath.Join(f.outDir, "scorecard.json"), jsonDoc, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write scorecard: %v\n", err)
			return 6
		}
		sarifDoc, _ := report.SARIF(sc, res.Findings)
		if err := os.WriteFile(filepath.Join(f.outDir, "drift.sarif"), sarifDoc, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write sarif: %v\n", err)
			return 6
		}
		if err := report.WriteHTML(filepath.Join(f.outDir, "scorecard.html"), sc, vr); err != nil {
			fmt.Fprintf(os.Stderr, "write html: %v\n", err)
			return 6
		}
	}

	switch strings.ToLower(f.format) {
	case "json":
		doc, _ := report.JSON(sc, vr, res.Findings)
		fmt.Println(string(doc))
	case "sarif":
		doc, _ := report.SARIF(sc, res.Findings)
		fmt.Println(string(doc))
	default:
		fmt.Print(report.Text(sc, vr, f.top))
	}

	// Gates.
	problems := sc.Enforce(score.Gate{
		MaxScore:       f.failUnder,
		FailOnCritical: f.failOnCritical,
		FailOnAny:      f.failOnAny,
	})
	if f.failOnResidualAWS && vr != nil && len(vr.ResidualAWS) > 0 {
		problems = append(problems, fmt.Sprintf(
			"%d residual AWS reference(s) survived generation", len(vr.ResidualAWS)))
	}
	if vr != nil && !vr.OK() && f.generateReports {
		problems = append(problems, "generated manifests failed validation")
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "\n  GATE FAILED")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "    ✗ "+p)
		}
		fmt.Fprintln(os.Stderr)
		return 1
	}
	if f.outDir != "" {
		fmt.Printf("  reports  %s/{scorecard.json,drift.sarif,scorecard.html}\n\n",
			strings.TrimSuffix(f.outDir, "/"))
	}
	return 0
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	kubectl := fs.String("kubectl", "", "kubectl binary to use")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig to use")
	requireKubectl := fs.Bool("require-kubectl", false, "fail if the kubectl gate cannot run")
	serverDryRun := fs.Bool("server-dry-run", false, "also run kubectl apply --dry-run=server")
	helmRender := fs.Bool("helm", true, "render Helm charts with helm template before validating")
	noHelm := fs.Bool("no-helm", false, "do not render Helm charts")
	requireHelm := fs.Bool("require-helm", false, "fail if a chart cannot be rendered")
	namespace := fs.String("namespace", "ground-system", "namespace used when rendering charts")
	asJSON := fs.Bool("json", false, "emit JSON")
	flagArgs, positional := reorderArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	dir := fs.Arg(0)
	if dir == "" && len(positional) > 0 {
		dir = positional[0]
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "validate needs a directory, e.g. `turion-platform-drift validate ./generated`")
		return 2
	}
	vr, err := validate.DirOpts(dir, validate.Options{
		Kubectl: *kubectl, Kubeconfig: *kubeconfig, RequireKubectl: *requireKubectl,
		ServerDryRun: *serverDryRun, Helm: *helmRender && !*noHelm, RequireHelm: *requireHelm,
		Namespace: *namespace,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
		return 3
	}
	if *asJSON {
		doc, _ := json.MarshalIndent(vr, "", "  ")
		fmt.Println(string(doc))
	} else {
		fmt.Printf("\n  turion-platform-drift validate  %s\n", dir)
		fmt.Println("  ─────────────────────────────────────────────────────────────────────────")
		fmt.Printf("  files      %d\n", vr.Files)
		fmt.Printf("  objects    %d\n", vr.Objects)
		for kind, n := range vr.KindCounts {
			fmt.Printf("             %-30s %d\n", kind, n)
		}
		fmt.Printf("  errors     %d\n", len(vr.Errors()))
		for _, iss := range vr.Errors() {
			fmt.Printf("             ✗ %s doc %d %s/%s: %s\n",
				iss.File, iss.Doc, iss.Kind, iss.Name, iss.Message)
		}
		warns := len(vr.Issues) - len(vr.Errors())
		if warns > 0 {
			fmt.Printf("  warnings   %d\n", warns)
			for _, iss := range vr.Issues {
				if iss.Severity != "warning" {
					continue
				}
				fmt.Printf("             · %s %s: %s\n", iss.Kind, iss.Name, iss.Message)
			}
		}
		if len(vr.ResidualAWS) > 0 {
			fmt.Printf("  residual AWS references  %d\n", len(vr.ResidualAWS))
			for _, r := range vr.ResidualAWS {
				fmt.Printf("             ✗ %s: %s\n", r.File, r.Message)
			}
		} else {
			fmt.Printf("  residual AWS references  0  ✓\n")
		}
		if vr.HelmRan {
			fmt.Printf("  helm       rendered %s\n", strings.Join(vr.HelmCharts, ", "))
		}
		if len(vr.Skipped) > 0 {
			fmt.Printf("  skipped    %s\n", strings.Join(vr.Skipped, ", "))
		}
		if vr.KubectlRan {
			state := "PASS"
			if !vr.KubectlOK {
				state = "FAIL"
			}
			fmt.Printf("  kubectl    %s  [%s]\n", vr.KubectlCmd, state)
			if len(vr.Unserved) > 0 {
				fmt.Printf("             not served by this cluster: %s\n", strings.Join(vr.Unserved, ", "))
			}
		} else {
			fmt.Printf("  kubectl    skipped (%s)\n", vr.KubectlSkip)
		}
		if vr.Mode != "" {
			fmt.Printf("  mode       %s\n", vr.Mode)
		}
		verdict := "PASS"
		if len(vr.Errors()) > 0 {
			verdict = "FAIL (schema)"
		} else if len(vr.ResidualAWS) > 0 {
			verdict = "FAIL (residual AWS references)"
		}
		fmt.Printf("\n  %s\n\n", verdict)
	}
	if !vr.OK() {
		return 1
	}
	return 0
}

func absOr(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
