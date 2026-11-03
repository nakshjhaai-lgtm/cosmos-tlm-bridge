package validate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidManifestsPass(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "all.yaml", `
apiVersion: v1
kind: Namespace
metadata:
  name: ground-system
---
apiVersion: batch/v1
kind: Job
metadata:
  name: pass-processor
  namespace: ground-system
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.ground.local/pass:1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: ground-system
spec:
  selector:
    matchLabels:
      app: api
  template:
    spec:
      containers:
        - name: c
          image: registry.ground.local/api:1
`)
	vr, err := hermetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(vr.Errors()) != 0 {
		t.Fatalf("unexpected errors: %v", vr.Errors())
	}
	if vr.Objects != 3 {
		t.Fatalf("objects = %d, want 3", vr.Objects)
	}
	if !vr.OK() {
		t.Fatal("report should be OK")
	}
}

func TestCatchesMissingRequiredFields(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bad.yaml", `
apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: ground-system
spec:
  replicas: 1
`)
	vr := hermeticMust(t, dir)
	if len(vr.Errors()) == 0 {
		t.Fatal("expected errors for a Deployment without a name/selector/template")
	}
	var msgs []string
	for _, e := range vr.Errors() {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, " | ")
	for _, want := range []string{"metadata.name", "spec.selector", "spec.template"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing error for %s (got %s)", want, joined)
		}
	}
}

func TestCatchesJobWithoutRestartPolicy(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "job.yaml", `
apiVersion: batch/v1
kind: Job
metadata:
  name: j
spec:
  template:
    spec:
      containers:
        - name: c
          image: x:1
`)
	vr := hermeticMust(t, dir)
	var found bool
	for _, e := range vr.Errors() {
		if strings.Contains(e.Message, "restartPolicy") {
			found = true
		}
	}
	if !found {
		t.Fatal("Job without restartPolicy should be rejected")
	}
}

func TestCatchesResidualAWS(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "drift.yaml", `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/api
spec:
  selector:
    matchLabels: {app: api}
  template:
    spec:
      containers:
        - name: c
          image: 123456789012.dkr.ecr.us-west-2.amazonaws.com/api:1
          env:
            - name: BUCKET
              value: s3://turion-tlm-archive-prod
`)
	vr := hermeticMust(t, dir)
	if len(vr.ResidualAWS) == 0 {
		t.Fatal("expected residual AWS findings")
	}
	var msgs []string
	for _, r := range vr.ResidualAWS {
		msgs = append(msgs, r.Message)
	}
	joined := strings.Join(msgs, " | ")
	for _, want := range []string{"ARN", "ECR", "IRSA", "s3://"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing residual class %q in %s", want, joined)
		}
	}
	if vr.OK() {
		t.Fatal("report must not be OK with residual AWS references")
	}
}

func TestSkipsHelmPackagingAndTemplates(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "chart/Chart.yaml", "apiVersion: v2\nname: x\nversion: 1.0.0\n")
	write(t, dir, "chart/values.yaml", "replicaCount: 2\n")
	write(t, dir, "chart/templates/job.yaml", "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: {{ .Release.Name }}\n")
	write(t, dir, "real.yaml", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ok\n")
	vr, err := hermetic(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(vr.Errors()) != 0 {
		t.Fatalf("helm files should be skipped, got %v", vr.Errors())
	}
	if len(vr.Skipped) != 3 {
		t.Fatalf("skipped = %v, want 3 entries", vr.Skipped)
	}
	if vr.Files != 1 {
		t.Fatalf("files = %d, want 1 manifest", vr.Files)
	}
}

func TestErrorsOnBadYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "broken.yaml", "apiVersion: v1\nkind: [unclosed\n")
	vr := hermeticMust(t, dir)
	if len(vr.Errors()) == 0 {
		t.Fatal("malformed YAML should produce an error")
	}
}

func TestErrorsWhenNoManifests(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "readme.txt", "nothing here")
	if _, err := hermetic(dir); err == nil {
		t.Fatal("expected an error when no manifests are found")
	}
}

func TestClusterScopedKindRejectsNamespace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ns.yaml", `
apiVersion: v1
kind: Namespace
metadata:
  name: ground-system
  namespace: ground-system
`)
	vr := hermeticMust(t, dir)
	var found bool
	for _, e := range vr.Errors() {
		if strings.Contains(e.Message, "cluster-scoped") {
			found = true
		}
	}
	if !found {
		t.Fatal("Namespace with metadata.namespace should be rejected")
	}
}

func TestUnknownCustomResourceWarnsNotErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "crd.yaml", `
apiVersion: ground.turion.space/v1alpha1
kind: GroundStackClaim
metadata:
  name: droid-ground-a
spec:
  parameters:
    namespace: ground-system
`)
	vr := hermeticMust(t, dir)
	if len(vr.Errors()) != 0 {
		t.Fatalf("custom resource should not error: %v", vr.Errors())
	}
	if len(vr.Issues) == 0 {
		t.Fatal("custom resource should still warn about its CRD")
	}
	if !strings.Contains(vr.Issues[0].Message, "CRD") {
		t.Errorf("warning should mention CRDs, got %q", vr.Issues[0].Message)
	}
}

// hermetic runs validation with kubectl disabled, so unit tests never depend on
// whether a cluster is reachable from the machine running them.
func hermetic(dir string) (*Report, error) {
	return DirOpts(dir, Options{Kubectl: "/nonexistent/kubectl"})
}

func hermeticMust(t *testing.T, dir string) *Report {
	t.Helper()
	rep, err := hermetic(dir)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return rep
}

// ---------------------------------------------------------------- kustomize
func TestKustomizeOverlayIsNotTreatedAsClusterObject(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "kustomize/kustomization.yaml", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
  - name: 123456789012.dkr.ecr.us-west-2.amazonaws.com/api
    newName: registry.ground.local/api
`)
	write(t, dir, "real.yaml", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ok\n")
	vr := hermeticMust(t, dir)
	if len(vr.Errors()) != 0 {
		t.Fatalf("unexpected errors: %v", vr.Errors())
	}
	if len(vr.ResidualAWS) != 0 {
		t.Fatalf("the overlay's rewrite patterns must not count as residual AWS: %v", vr.ResidualAWS)
	}
	if vr.Objects != 1 {
		t.Fatalf("objects = %d, want 1 (the overlay is not an object)", vr.Objects)
	}
	joined := strings.Join(vr.Skipped, ",")
	if !strings.Contains(joined, "kustomize overlay") {
		t.Errorf("overlay should be reported as skipped, got %v", vr.Skipped)
	}
}

// ---------------------------------------------------------------- helm
func TestHelmChartsAreRenderedAndValidated(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	write(t, dir, "chart/Chart.yaml", "apiVersion: v2\nname: ground\nversion: 0.1.0\n")
	write(t, dir, "chart/values.yaml", "bucket: tlm-archive\nimage: ground/api:1\n")
	write(t, dir, "chart/templates/job.yaml", `apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .Release.Name }}-processor
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: {{ .Values.image }}
`)
	vr, err := DirOpts(dir, Options{Helm: true, Namespace: "ground-system", Kubectl: "/nonexistent/kubectl"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !vr.HelmRan {
		t.Fatalf("expected helm to render the chart; skipped=%v issues=%v", vr.Skipped, vr.Issues)
	}
	if len(vr.HelmCharts) != 1 {
		t.Fatalf("charts = %v, want 1", vr.HelmCharts)
	}
	if len(vr.Errors()) != 0 {
		t.Fatalf("rendered chart has errors: %v", vr.Errors())
	}
	if vr.Objects < 1 {
		t.Fatal("rendered chart produced no objects")
	}
}

func TestHelmRenderCatchesBrokenTemplate(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	write(t, dir, "chart/Chart.yaml", "apiVersion: v2\nname: broken\nversion: 0.1.0\n")
	write(t, dir, "chart/templates/bad.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.missing | badFunc }}\n")
	vr, err := DirOpts(dir, Options{Helm: true, Kubectl: "/nonexistent/kubectl"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, iss := range vr.Errors() {
		if strings.Contains(iss.Message, "helm template failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a chart that fails to render must be a hard error, got %v", vr.Issues)
	}
}

func TestRequireHelmFailsWithoutCharts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: x\n")
	vr, _ := DirOpts(dir, Options{Helm: true, RequireHelm: true, Kubectl: "/nonexistent/kubectl"})
	if len(vr.Errors()) == 0 {
		t.Fatal("--require-helm with no chart should fail")
	}
}

// helm writes warnings to stderr (kubeconfig permissions, deprecated values,
// kube-version notes). They must never enter the manifest stream, or the first
// rendered document becomes a bogus map with no apiVersion.
func TestHelmStderrNoiseDoesNotBecomeAManifest(t *testing.T) {
	bin := t.TempDir()
	stub := `#!/bin/sh
echo "WARNING: Kubernetes configuration file is group-readable. This is insecure." >&2
echo "WARNING: another note on stderr" >&2
cat <<'YAML'
---
# Source: chart/templates/job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: stub-processor
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.ground.local/ground/stub:1
YAML
`
	write(t, bin, "helm", stub)
	if err := os.Chmod(filepath.Join(bin, "helm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	write(t, dir, "chart/Chart.yaml", "apiVersion: v2\nname: stub\nversion: 0.1.0\n")
	write(t, dir, "chart/templates/job.yaml", `apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .Chart.Name }}-stub
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.ground.local/ground/stub:1
`)

	vr, err := DirOpts(dir, Options{Helm: true, Namespace: "ground-system", Kubectl: "/nonexistent/kubectl"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !vr.HelmRan {
		t.Fatalf("stub helm was not used; skipped=%v issues=%v", vr.Skipped, vr.Issues)
	}
	if errs := vr.Errors(); len(errs) != 0 {
		t.Fatalf("stderr noise leaked into the manifest stream: %v", errs)
	}
	if vr.KindCounts["Job"] != 1 {
		t.Fatalf("rendered Job not counted: %v", vr.KindCounts)
	}
}
