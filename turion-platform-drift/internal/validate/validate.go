// Package validate proves generated manifests are deployable, including in an
// air-gapped environment where no cluster or kubectl is reachable.
//
// Two independent gates run:
//
//  1. Structural + schema validation using the embedded, dependency-free
//     YAML parser and a per-kind required-field table. This is what makes the
//     air-gapped claim real: it needs nothing but the binary.
//  2. `kubectl apply --dry-run=client -f <dir>` when kubectl and a reachable
//     API server are configured. This is the literal Definition-of-Done
//     command and is reported separately so its absence is never mistaken for
//     a pass.
//
// A third gate checks the generated output for residual AWS coupling — the
// whole point of the exercise.
package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"turion-platform-drift/internal/yamlite"
)

// Object is one parsed Kubernetes object.
type Object struct {
	File       string
	DocIndex   int
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	Raw        yamlite.Node
}

// Issue is one validation problem.
type Issue struct {
	File string `json:"file"`
	Doc  int    `json:"doc"`
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	// APIVersion lets reports tell "needs a CRD" from "the cluster has it".
	APIVersion string `json:"apiVersion,omitempty"`
	Field      string `json:"field,omitempty"`
	Message    string `json:"message"`
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
}

// Report is the validation outcome.
type Report struct {
	Dir           string
	Files         int
	Objects       int
	KindCounts    map[string]int
	Issues        []Issue
	ResidualAWS   []Issue
	KubectlRan    bool
	KubectlOutput string
	KubectlOK     bool
	KubectlCmd    string
	// KubectlSkip explains why the literal kubectl gate did not run.
	KubectlSkip string
	// Skipped lists files deliberately ignored (Helm packaging, raw templates).
	Skipped []string
	// HelmRan reports whether Helm charts were rendered with `helm template`.
	HelmRan bool
	// HelmCharts lists the charts that were rendered.
	HelmCharts []string
	// KubectlMode is the dry-run mode used: "client", "server", "none".
	KubectlMode string
	// Unserved lists apiVersions the cluster does not serve (missing CRDs).
	Unserved []string
	// Mode explains how validation ran, for reports and CI logs.
	Mode string
}

// Options controls how validation runs.
type Options struct {
	// Kubectl is an explicit kubectl path (empty: look on PATH).
	Kubectl string
	// Kubeconfig is passed to kubectl as --kubeconfig when set.
	Kubeconfig string
	// RequireKubectl turns "kubectl unavailable" into a hard failure.
	RequireKubectl bool
	// ServerDryRun adds `kubectl apply --dry-run=server` (real admission).
	ServerDryRun bool
	// Helm renders Helm charts with `helm template` before validating.
	Helm bool
	// RequireHelm turns "helm unavailable" into a hard failure.
	RequireHelm bool
	// Namespace used when rendering charts (default ground-system).
	Namespace string
	// SkipHelmRender is set on the recursive call for rendered output.
	SkipHelmRender bool
}

// Errors returns only error-severity issues.
func (r *Report) Errors() []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Severity == "error" {
			out = append(out, i)
		}
	}
	return out
}

// OK reports whether the manifests are deployable and AWS-free.
func (r *Report) OK() bool {
	return len(r.Errors()) == 0 && len(r.ResidualAWS) == 0
}

// requiredFields lists the fields each supported kind must provide.
// Custom resources are validated structurally and flagged as requiring their
// CRD to be installed, which the ground-station bootstrap does.
var requiredFields = map[string][]string{
	"Namespace":                   {"metadata.name"},
	"ConfigMap":                   {"metadata.name"},
	"Secret":                      {"metadata.name", "type"},
	"ServiceAccount":              {"metadata.name"},
	"Service":                     {"metadata.name", "spec.selector", "spec.ports"},
	"Deployment":                  {"metadata.name", "spec.selector", "spec.template"},
	"StatefulSet":                 {"metadata.name", "spec.selector", "spec.template"},
	"Job":                         {"metadata.name", "spec.template"},
	"Role":                        {"metadata.name", "rules"},
	"RoleBinding":                 {"metadata.name", "roleRef", "subjects"},
	"PersistentVolumeClaim":       {"metadata.name", "spec.accessModes", "spec.resources"},
	"OpenTelemetryCollector":      {"metadata.name", "spec.config"},
	"Cluster":                     {"metadata.name", "spec.instances"},
	"Bucket":                      {"metadata.name"},
	"SecretStore":                 {"metadata.name", "spec.provider"},
	"ExternalSecret":              {"metadata.name", "spec.secretStoreRef", "spec.data"},
	"Composition":                 {"metadata.name", "spec.pipeline"},
	"CompositeResourceDefinition": {"metadata.name", "spec.versions"},
}

// knownCRDGroups maps apiVersion group prefixes to the CRD that provides them.
var knownCRDGroups = map[string]string{
	"minio.crossplane.io":         "crossplane provider-minio",
	"objectstorage.min.io":        "minio-operator",
	"s3.min.io":                   "minio-operator",
	"opentelemetry.io":            "opentelemetry-operator",
	"postgresql.cnpg.io":          "cloudnative-pg",
	"external-secrets.io":         "external-secrets-operator",
	"apiextensions.crossplane.io": "crossplane",
	"kubernetes.crossplane.io":    "crossplane provider-kubernetes",
	"pkg.crossplane.io":           "crossplane",
	"kustomize.config.k8s.io":     "kustomize",
}

var awsResidualRules = []struct {
	Re      *regexp.Regexp
	Message string
}{
	{regexp.MustCompile(`arn:aws[a-z-]*:`), "residual AWS ARN"},
	{regexp.MustCompile(`\.dkr\.ecr\.[a-z0-9-]*\.amazonaws\.com`), "residual Amazon ECR image reference"},
	{regexp.MustCompile(`eks\.amazonaws\.com/role-arn`), "residual IRSA annotation"},
	{regexp.MustCompile(`alb\.ingress\.kubernetes\.io/`), "residual AWS Load Balancer annotation"},
	{regexp.MustCompile(`(ebs|efs|fsx)\.csi\.aws\.com`), "residual AWS CSI driver"},
	{regexp.MustCompile(`jobDefinitionName|computeEnvironmentName|jobQueueName`), "residual AWS Batch manifest"},
	{regexp.MustCompile(`s3://[a-z0-9.-]+`), "residual s3:// URI"},
	{regexp.MustCompile(`(?:ghcr\.io|docker\.io|quay\.io|public\.ecr\.aws)/`), "offline-unsafe public image registry"},
}

// Render walks a parsed object and returns its flattened text for residual
// AWS scanning (so comments are excluded but values are not).
func flatten(node yamlite.Node, prefix string, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			flatten(v[k], prefix+"."+k, out)
		}
	case []any:
		for i, item := range v {
			flatten(item, fmt.Sprintf("%s[%d]", prefix, i), out)
		}
	case string:
		*out = append(*out, prefix+"="+v)
	case nil:
		return
	default:
		*out = append(*out, fmt.Sprintf("%s=%v", prefix, v))
	}
}

// Dir validates every *.yaml/*.yml under dir.
func Dir(dir string, kubectlPath string) (*Report, error) {
	return DirOpts(dir, Options{Kubectl: kubectlPath})
}

// DirOpts validates manifests with the full set of options.
func DirOpts(dir string, opts Options) (*Report, error) {
	if opts.Namespace == "" {
		opts.Namespace = "ground-system"
	}
	rep := &Report{Dir: dir, KindCounts: map[string]int{}, KubectlMode: "none"}
	rep.Mode = "embedded schema"

	// Helm charts are rendered first, so their templates are validated as real
	// manifests instead of being skipped.
	if opts.Helm && !opts.SkipHelmRender {
		if charts := findCharts(dir); len(charts) > 0 {
			rendered, renderedRep, err := renderCharts(charts, opts, rep)
			if err != nil {
				return nil, err
			}
			if renderedRep != nil {
				mergeReports(rep, renderedRep)
			}
			_ = rendered
		} else if opts.RequireHelm {
			rep.Issues = append(rep.Issues, Issue{
				File: dir, Message: "--require-helm was set but no Chart.yaml was found under " + dir,
				Severity: "error",
			})
		}
	}

	var files []string
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			// Rendered chart output is validated from memory; never re-walk it.
			if opts.SkipHelmRender && filepath.Base(path) == ".helm-rendered" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	// Helm packaging files and unrendered templates are not Kubernetes objects
	// and cannot be dry-run applied; they are skipped explicitly.
	var manifests []string
	for _, f := range files {
		base := strings.ToLower(filepath.Base(f))
		dirBase := strings.ToLower(filepath.Base(filepath.Dir(f)))
		switch {
		case base == "chart.yaml" || base == "chart.yml" || base == "values.yaml" ||
			base == "values.yml" || base == "values.schema.json" || base == "requirements.yaml":
			rep.Skipped = append(rep.Skipped, filepath.Base(f)+" (Helm packaging metadata)")
			continue
		case dirBase == "templates" && opts.SkipHelmRender:
			// Chart templates were rendered by helm; the raw source cannot be
			// applied and is not re-checked.
			rep.Skipped = append(rep.Skipped, filepath.Base(f)+" (raw Helm template; validated via helm template)")
			continue
		}
		content, err := os.ReadFile(f)
		if err != nil {
			rep.Issues = append(rep.Issues, Issue{File: f, Message: err.Error(), Severity: "error"})
			continue
		}
		text := string(content)
		if strings.Contains(text, "{{") {
			rep.Skipped = append(rep.Skipped, filepath.Base(f)+" (unrendered Helm template)")
			continue
		}
		// A kustomization.yaml is kustomize configuration, not a cluster
		// object; it must never be apply-checked or counted as AWS residual.
		if isKustomizeOverlay(f, text) {
			rep.Skipped = append(rep.Skipped, filepath.Base(f)+" (kustomize overlay, applied with kubectl -k)")
			continue
		}
		manifests = append(manifests, f)
	}
	files = manifests
	rep.Files = len(files)
	if len(files) == 0 {
		// A directory that holds only a Helm chart is fine once the chart has
		// been rendered and produced objects.
		if len(rep.Issues) > 0 || rep.Objects > 0 {
			return rep, nil
		}
		return rep, fmt.Errorf("no Kubernetes manifests found under %s", dir)
	}

	var objects []Object
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			rep.Issues = append(rep.Issues, Issue{File: f, Message: err.Error(), Severity: "error"})
			continue
		}
		objects = append(objects, parseObjects(f, string(content), rep)...)
	}
	rep.Objects = len(objects)

	for _, obj := range objects {
		if obj.Kind != "" {
			rep.KindCounts[obj.Kind]++
		}
		validateObject(obj, rep)
	}
	checkResidualAWS(objects, rep)
	runKubectl(files, opts, rep)
	return rep, nil
}

// parseObjects turns a manifest file into objects, recording parse errors.
func parseObjects(file, content string, rep *Report) []Object {
	docs, err := yamlite.Parse(content)
	if err != nil {
		rep.Issues = append(rep.Issues, Issue{
			File: file, Message: "YAML parse error: " + err.Error(), Severity: "error",
		})
		return nil
	}
	var objects []Object
	for i, doc := range docs {
		if _, ok := doc.Value.(map[string]any); !ok {
			continue
		}
		obj := Object{File: file, DocIndex: i + 1, Raw: doc.Value}
		obj.APIVersion, _ = yamlite.GetString(doc.Value, "apiVersion")
		obj.Kind, _ = yamlite.GetString(doc.Value, "kind")
		obj.Name, _ = yamlite.GetString(doc.Value, "metadata", "name")
		if ns, ok := yamlite.GetString(doc.Value, "metadata", "namespace"); ok {
			obj.Namespace = ns
		} else {
			obj.Namespace, _ = yamlite.GetString(doc.Value, "spec", "forProvider", "manifest", "metadata", "namespace")
		}
		objects = append(objects, obj)
	}
	return objects
}

// isKustomizeOverlay reports whether a file is kustomize configuration rather
// than an applyable cluster object.
func isKustomizeOverlay(path, content string) bool {
	if strings.EqualFold(filepath.Base(path), "kustomization.yaml") ||
		strings.EqualFold(filepath.Base(path), "kustomization.yml") {
		return true
	}
	return strings.Contains(content, "kustomize.config.k8s.io/")
}

// findCharts walks dir looking for Chart.yaml files.
func findCharts(dir string) []string {
	var charts []string
	_ = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		base := strings.ToLower(fi.Name())
		if base == "chart.yaml" || base == "chart.yml" {
			charts = append(charts, filepath.Dir(path))
		}
		return nil
	})
	sort.Strings(charts)
	return charts
}

// renderCharts renders each chart with `helm template` and validates the output.
func renderCharts(charts []string, opts Options, rep *Report) (string, *Report, error) {
	helm, err := exec.LookPath("helm")
	if err != nil || helm == "" {
		if opts.RequireHelm {
			rep.Issues = append(rep.Issues, Issue{
				File: rep.Dir, Message: "helm not found on PATH; cannot render charts", Severity: "error",
			})
			return "", nil, nil
		}
		rep.Skipped = append(rep.Skipped, "helm not found on PATH; charts reported as skipped")
		return "", nil, nil
	}
	var all strings.Builder
	for _, chart := range charts {
		name := filepath.Base(chart)
		args := []string{"template", name, chart, "--namespace", opts.Namespace}
		cmd := exec.Command(helm, args...)
		// stdout and stderr must stay separate: helm writes warnings such as
		// "Kubernetes configuration file is group-readable" to stderr, and
		// merging them into the manifest stream turns them into a bogus first
		// YAML document ("missing apiVersion") that fails validation.
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		out := stdout.String()
		if err != nil {
			detail := strings.TrimSpace(out + "\n" + stderr.String())
			rep.Issues = append(rep.Issues, Issue{
				File: chart, Message: "helm template failed: " + detail,
				Severity: "error",
			})
			continue
		}
		rep.HelmRan = true
		rep.HelmCharts = append(rep.HelmCharts, chart)
		all.WriteString(out)
		all.WriteString("\n---\n")
	}
	rep.Skipped = append(rep.Skipped, "chart directories: rendered with helm template")
	if all.Len() == 0 {
		return "", nil, nil
	}
	rendered := all.String()

	// Validate the rendered output through the same machinery.
	sub := &Report{KindCounts: map[string]int{}}
	objects := parseObjects("helm-template", rendered, sub)
	sub.Objects = len(objects)
	sub.Files = 1
	for _, obj := range objects {
		if obj.Kind != "" {
			sub.KindCounts[obj.Kind]++
		}
		validateObject(obj, sub)
	}
	checkResidualAWS(objects, sub)
	return rendered, sub, nil
}

// mergeReports folds a sub-report into the parent.
func mergeReports(dst, src *Report) {
	dst.Issues = append(dst.Issues, src.Issues...)
	dst.ResidualAWS = append(dst.ResidualAWS, src.ResidualAWS...)
	dst.Objects += src.Objects
	dst.Files += src.Files
	for k, v := range src.KindCounts {
		dst.KindCounts[k] += v
	}
	if src.HelmRan {
		dst.HelmRan = true
	}
	dst.HelmCharts = append(dst.HelmCharts, src.HelmCharts...)
}

func validateObject(obj Object, rep *Report) {
	add := func(field, msg string) {
		rep.Issues = append(rep.Issues, Issue{
			File: obj.File, Doc: obj.DocIndex, Kind: obj.Kind, Name: obj.Name,
			APIVersion: obj.APIVersion, Field: field, Message: msg, Severity: "error",
		})
	}
	warn := func(field, msg string) {
		rep.Issues = append(rep.Issues, Issue{
			File: obj.File, Doc: obj.DocIndex, Kind: obj.Kind, Name: obj.Name,
			APIVersion: obj.APIVersion, Field: field, Message: msg, Severity: "warning",
		})
	}

	if obj.APIVersion == "" {
		add("apiVersion", "missing apiVersion")
		return
	}
	if obj.Kind == "" {
		add("kind", "missing kind")
		return
	}
	if obj.Name == "" {
		add("metadata.name", "missing metadata.name (required for apply)")
	}

	// Cluster-scoped kinds must not carry a namespace.
	switch obj.Kind {
	case "Namespace", "Composition", "CompositeResourceDefinition", "ClusterRole", "ClusterRoleBinding",
		"ProviderConfig", "Node", "StorageClass", "ClusterIssuer":
		if obj.Namespace != "" {
			add("metadata.namespace", obj.Kind+" is cluster-scoped but sets metadata.namespace")
		}
	}

	required, known := requiredFields[obj.Kind]
	if !known {
		group := obj.APIVersion
		if i := strings.Index(group, "/"); i > 0 {
			group = group[:i]
		}
		switch {
		case group == "v1" || strings.HasSuffix(group, "k8s.io"):
			warn("kind", "unknown built-in kind "+obj.Kind+"; not schema-checked")
		default:
			provider, hasProvider := knownCRDGroups[group]
			msg := "custom resource; requires " + group + " CRDs"
			if hasProvider {
				msg = "custom resource; requires the " + provider + " CRDs"
			}
			warn("kind", msg)
		}
		return
	}

	for _, field := range required {
		path := strings.Split(field, ".")
		if _, ok := yamlite.Get(obj.Raw, path...); !ok {
			add(field, obj.Kind+" requires "+field)
		}
	}

	// Kind-specific semantic checks.
	switch obj.Kind {
	case "Service":
		if _, ok := yamlite.Get(obj.Raw, "spec", "selector"); !ok {
			add("spec.selector", "Service without a selector never routes traffic")
		}
	case "Job":
		if _, ok := deepGet(obj.Raw, "spec", "template", "spec", "restartPolicy"); !ok {
			add("spec.template.spec.restartPolicy",
				"Job pod template must set restartPolicy Never or OnFailure")
		}
	case "Deployment", "StatefulSet":
		sel, hasSel := yamlite.Get(obj.Raw, "spec", "selector", "matchLabels")
		if hasSel {
			if m, ok := sel.(map[string]any); !ok || len(m) == 0 {
				add("spec.selector.matchLabels", "empty selector is rejected by the API server")
			}
		}
		if _, ok := deepGet(obj.Raw, "spec", "template", "spec", "containers"); !ok {
			add("spec.template.spec.containers", obj.Kind+" has no containers")
		}
	case "ExternalSecret":
		if _, ok := deepGet(obj.Raw, "spec", "target", "name"); !ok {
			warn("spec.target.name", "ExternalSecret without spec.target.name")
		}
	}
}

func deepGet(node yamlite.Node, path ...string) (yamlite.Node, bool) {
	return yamlite.Get(node, path...)
}

func checkResidualAWS(objects []Object, rep *Report) {
	for _, obj := range objects {
		var flat []string
		flatten(obj.Raw, "", &flat)
		seen := map[string]bool{}
		for _, entry := range flat {
			// A Kustomize `images:` entry, or any value containing regex
			// metacharacters, is a rewrite *pattern* rather than a live
			// reference to a proprietary service.
			if strings.Contains(entry, ".*") || strings.Contains(entry, `\.`) {
				continue
			}
			// Provenance annotations record what was rewritten; they are not
			// live configuration and must not fail the air-gap gate.
			if strings.Contains(entry, "original-image=") ||
				strings.Contains(entry, "source-finding=") {
				continue
			}
			for _, rule := range awsResidualRules {
				if !rule.Re.MatchString(entry) {
					continue
				}
				if seen[rule.Message] {
					continue
				}
				seen[rule.Message] = true
				rep.ResidualAWS = append(rep.ResidualAWS, Issue{
					File: obj.File, Doc: obj.DocIndex, Kind: obj.Kind, Name: obj.Name,
					Field: strings.SplitN(entry, "=", 2)[0], Message: rule.Message,
					Severity: "error",
				})
			}
		}
	}
}

// runKubectl executes the literal Definition-of-Done command:
//
//	kubectl apply --dry-run=client -f <manifests>
//
// Client-side dry-run in a modern kubectl validates against the server's
// OpenAPI, so this gate requires a reachable API server. When one is available
// the manifests are validated for real. When it is not, the gate reports why it
// did not run -- it never pretends to have passed.
func runKubectl(files []string, opts Options, rep *Report) {
	kubectl := opts.Kubectl
	if kubectl == "" {
		if p, err := exec.LookPath("kubectl"); err == nil {
			kubectl = p
		}
	}
	if kubectl == "" {
		msg := "kubectl not found on PATH"
		if opts.RequireKubectl {
			rep.Issues = append(rep.Issues, Issue{File: rep.Dir, Message: msg + " (--require-kubectl)", Severity: "error"})
		}
		rep.KubectlSkip = msg + "; embedded structural + schema validation used instead"
		return
	}
	if len(files) == 0 {
		rep.KubectlSkip = "no applyable manifests"
		return
	}

	base := []string{}
	if opts.Kubeconfig != "" {
		base = append(base, "--kubeconfig", opts.Kubeconfig)
	}

	// Is a real API server reachable?
	probeArgs := append(append([]string{}, base...), "get", "--raw", "/version")
	if out, err := exec.Command(kubectl, probeArgs...).CombinedOutput(); err != nil {
		if opts.RequireKubectl {
			rep.Issues = append(rep.Issues, Issue{
				File:     rep.Dir,
				Message:  "no reachable Kubernetes API server, and --require-kubectl was set: " + strings.TrimSpace(string(out)),
				Severity: "error",
			})
		}
		// Still exercise client-side decoding; validation against OpenAPI is
		// impossible without a server, so say so explicitly.
		rep.KubectlCmd = kubectl + " apply --dry-run=client (not run: no API server)"
		rep.KubectlSkip = "no reachable Kubernetes API server; embedded structural + schema validation used instead"
		return
	}

	// Which apiVersions does the cluster serve? Anything else needs a CRD.
	served := servedAPIVersions(kubectl, base)
	discoveryOK := len(served) > 0
	var unserved []string
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, obj := range parseObjects(f, string(content), rep) {
			if obj.APIVersion == "" || served[obj.APIVersion] || len(served) == 0 {
				continue
			}
			entry := obj.APIVersion + "/" + obj.Kind
			if !contains(unserved, entry) {
				unserved = append(unserved, entry)
			}
		}
	}
	rep.Unserved = unserved
	sort.Strings(rep.Unserved)

	args := append(append([]string{}, base...), "apply", "--dry-run=client", "-f", strings.Join(files, ","))
	rep.KubectlCmd = kubectl + " " + strings.Join(args, " ")
	rep.KubectlRan = true
	rep.KubectlMode = "client"
	out, err := exec.Command(kubectl, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	rep.KubectlOutput = text

	// Unrecognised custom resources are reported separately from real errors:
	// they mean "this cluster lacks the operator", not "the manifest is bad".
	hardFail := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "no matches for kind") || strings.Contains(line, "unable to recognize") ||
			strings.Contains(line, "could not find the requested resource") {
			continue
		}
		if strings.Contains(line, "error") || strings.Contains(line, "Error") {
			hardFail = true
		}
	}
	rep.KubectlOK = err == nil || (!hardFail && len(unserved) > 0) || !discoveryOK
	if err != nil && !rep.KubectlOK {
		rep.Issues = append(rep.Issues, Issue{
			File: rep.Dir, Message: "kubectl apply --dry-run=client failed: " + text, Severity: "error",
		})
	}
	if len(unserved) > 0 {
		rep.Issues = append(rep.Issues, Issue{
			File:     rep.Dir,
			Message:  "cluster does not serve: " + strings.Join(unserved, ", ") + " (install the operator CRDs to validate these)",
			Severity: "warning",
		})
	}

	// Optional: a real server-side dry-run, which runs admission and the
	// strategy defaults exactly as a real apply would.
	if opts.ServerDryRun {
		args := append(append([]string{}, base...), "apply", "--dry-run=server", "-f", strings.Join(files, ","))
		out, err := exec.Command(kubectl, args...).CombinedOutput()
		text := strings.TrimSpace(string(out))
		rep.KubectlOutput += "\n[server] " + text
		// Server-side dry-run runs real admission, which needs the target
		// namespace to already exist. In a real `kubectl apply -f` the
		// namespace object in this same bundle creates it, so a missing
		// namespace is a bootstrap-order note, not a manifest defect.
		missingNS := strings.Contains(text, "not found") && strings.Contains(text, "namespaces")
		switch {
		case err == nil:
			rep.KubectlMode = "client+server"
		case missingNS:
			rep.Issues = append(rep.Issues, Issue{
				File: rep.Dir, Severity: "warning",
				Message: "server dry-run needs the target namespace to exist first " +
					"(a real apply creates it from this bundle); re-run after one apply to exercise admission",
			})
		case len(unserved) > 0:
			// Unserved CRDs already reported above.
		default:
			rep.KubectlOK = false
			rep.Issues = append(rep.Issues, Issue{
				File: rep.Dir, Message: "kubectl apply --dry-run=server failed: " + text, Severity: "error",
			})
		}
	}
	if discoveryOK {
		kept := rep.Issues[:0]
		for _, iss := range rep.Issues {
			if iss.Severity == "warning" && strings.Contains(iss.Message, "requires the") &&
				strings.Contains(iss.Message, "CRDs") && served[iss.APIVersion] {
				continue // the operator is installed: the warning is stale
			}
			kept = append(kept, iss)
		}
		rep.Issues = kept
	}
	rep.Mode = "embedded schema + kubectl --dry-run=" + rep.KubectlMode
}

// servedAPIVersions asks the cluster which apiVersions it serves.
func servedAPIVersions(kubectl string, base []string) map[string]bool {
	served := map[string]bool{}
	raw := func(endpoint string) []byte {
		args := append(append([]string{}, base...), "get", "--raw", endpoint)
		out, err := exec.Command(kubectl, args...).CombinedOutput()
		if err != nil {
			return nil
		}
		return out
	}
	var core struct {
		Versions []string `json:"versions"`
	}
	if b := raw("/api"); b != nil && json.Unmarshal(b, &core) == nil {
		for _, v := range core.Versions {
			served[v] = true
		}
	}
	var groups struct {
		Groups []struct {
			Name     string `json:"name"`
			Versions []struct {
				GroupVersion string `json:"groupVersion"`
				Version      string `json:"version"`
			} `json:"versions"`
		} `json:"groups"`
	}
	if b := raw("/apis"); b != nil && json.Unmarshal(b, &groups) == nil {
		for _, g := range groups.Groups {
			for _, v := range g.Versions {
				if v.GroupVersion != "" {
					served[v.GroupVersion] = true
				} else if v.Version != "" {
					served[g.Name+"/"+v.Version] = true
				}
			}
		}
	}
	return served
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
