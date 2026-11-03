package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"turion-platform-drift/internal/scan"
	"turion-platform-drift/internal/validate"
)

func scanFixture(t *testing.T) *scan.Result {
	t.Helper()
	root := filepath.Join("..", "..", "ground-service")
	res, err := scan.Run(scan.Options{Root: root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return res
}

func build(t *testing.T, res *scan.Result, opts Options) *Result {
	t.Helper()
	if opts.OutDir == "" {
		opts.OutDir = t.TempDir()
	}
	g, err := Build(res, opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return g
}

// builtinKinds are the API kinds every conformant Kubernetes cluster serves
// with no operator installed. A --target kubernetes bundle must contain
// nothing else, because that is what makes it deployable air-gapped.
var builtinKinds = map[string]bool{
	"Namespace": true, "ConfigMap": true, "Secret": true, "ServiceAccount": true,
	"Service": true, "Deployment": true, "StatefulSet": true, "Job": true,
	"CronJob": true, "DaemonSet": true, "Role": true, "RoleBinding": true,
	"ClusterRole": true, "ClusterRoleBinding": true, "PersistentVolumeClaim": true,
	"Ingress": true, "NetworkPolicy": true, "PodDisruptionBudget": true,
	"HorizontalPodAutoscaler": true, "ServiceMonitor": false, "Kustomization": false,
}

func kindsIn(bundle string) []string {
	var out []string
	for _, line := range strings.Split(bundle, "\n") {
		if strings.HasPrefix(line, "kind: ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "kind: ")))
		}
	}
	return out
}

func TestKubernetesTargetUsesOnlyBuiltinKinds(t *testing.T) {
	res := scanFixture(t)
	g := build(t, res, Options{Target: "kubernetes", Namespace: "ground-system"})
	var bundle string
	for _, a := range g.Artifacts {
		if a.Standalone {
			continue
		}
		bundle += a.YAML
	}
	kinds := kindsIn(bundle)
	if len(kinds) < 10 {
		t.Fatalf("expected a full stack, got %d objects: %v", len(kinds), kinds)
	}
	for _, k := range kinds {
		if !builtinKinds[k] {
			t.Errorf("kubernetes target emitted non-builtin kind %q; the air-gapped "+
				"bundle must apply to a bare cluster with no CRDs", k)
		}
	}
	// The pieces that matter must be present.
	var have = map[string]bool{}
	for _, k := range kinds {
		have[k] = true
	}
	for _, want := range []string{"Namespace", "Job", "Deployment", "Service",
		"StatefulSet", "ConfigMap", "Secret", "ServiceAccount", "Role", "RoleBinding",
		"PersistentVolumeClaim"} {
		if !have[want] {
			t.Errorf("kubernetes target is missing %s", want)
		}
	}
}

func TestCrossplaneTargetUsesRealAPIGroups(t *testing.T) {
	res := scanFixture(t)
	g := build(t, res, Options{Target: "crossplane"})
	var bundle string
	for _, a := range g.Artifacts {
		if a.Standalone {
			continue
		}
		bundle += a.YAML
	}
	kinds := kindsIn(bundle)
	var have = map[string]bool{}
	for _, k := range kinds {
		have[k] = true
	}
	for _, want := range []string{"Bucket", "Cluster", "OpenTelemetryCollector",
		"SecretStore", "ExternalSecret", "Composition", "CompositeResourceDefinition"} {
		if !have[want] {
			t.Errorf("crossplane target is missing %s", want)
		}
	}
	// API groups verified against the upstream CRDs; stale groups are rejected
	// by a real API server (external-secrets removed v1beta1, crossplane v2
	// removed spec.resources).
	for _, want := range []string{
		"apiVersion: minio.crossplane.io/v1",
		"apiVersion: postgresql.cnpg.io/v1",
		"apiVersion: opentelemetry.io/v1beta1",
		"apiVersion: external-secrets.io/v1\n",
		// Composition v1 is the only served version in Crossplane 2.4.3.
		"apiVersion: apiextensions.crossplane.io/v1\nkind: Composition",
		// The XRD stays on v1 because v2 rejects claims, which this stack uses.
		"apiVersion: apiextensions.crossplane.io/v1\nkind: CompositeResourceDefinition",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("crossplane target missing current API group %q", want)
		}
	}
	for _, stale := range []string{"external-secrets.io/v1beta1", "objectstorage.min.io"} {
		if strings.Contains(bundle, stale) {
			t.Errorf("crossplane target emits stale API group %q that no current cluster serves", stale)
		}
	}
}

func TestCrossplaneTargetEmitsComposition(t *testing.T) {
	res := scanFixture(t)
	g := build(t, res, Options{Target: "crossplane"})
	var bundle string
	for _, a := range g.Artifacts {
		bundle += a.YAML
	}
	if !strings.Contains(bundle, "kind: Composition") {
		t.Error("missing Crossplane Composition")
	}
	if !strings.Contains(bundle, "mode: Pipeline") || !strings.Contains(bundle, "functionRef:") {
		t.Error("Crossplane v2 requires a pipeline Composition; spec.resources is rejected")
	}
	if strings.Contains(bundle, "patchSets:") {
		t.Error("spec.patchSets was removed in Crossplane v2")
	}
	if !strings.Contains(bundle, "kind: CompositeResourceDefinition") {
		t.Error("missing Crossplane XRD")
	}
}

func TestObjectStoreIsSelectable(t *testing.T) {
	res := scanFixture(t)
	for _, impl := range []string{"versitygw", "minio"} {
		g := build(t, res, Options{ObjectStore: impl})
		var bundle string
		for _, a := range g.Artifacts {
			bundle += a.YAML
		}
		marker := map[string]string{"versitygw": "versitygw", "minio": "minio/minio"}[impl]
		if !strings.Contains(bundle, marker) {
			t.Errorf("object store %q not reflected in output", impl)
		}
	}
}

func TestGeneratedManifestsValidate(t *testing.T) {
	res := scanFixture(t)
	dir := t.TempDir()
	if _, err := Build(res, Options{OutDir: dir, Namespace: "ground-system", Target: "crossplane"}); err != nil {
		t.Fatal(err)
	}
	vr, err := validate.DirOpts(dir, validate.Options{Kubectl: "/nonexistent/kubectl"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(vr.Errors()) != 0 {
		t.Fatalf("generated manifests have %d validation errors: %v", len(vr.Errors()), vr.Errors())
	}
	if len(vr.ResidualAWS) != 0 {
		t.Fatalf("generated manifests still reference AWS: %v", vr.ResidualAWS)
	}
	if vr.Objects < 15 {
		t.Fatalf("expected a substantial stack, got %d objects", vr.Objects)
	}
	t.Logf("validated %d generated objects across %d kinds", vr.Objects, len(vr.KindCounts))
}

func TestGeneratedImagesUseGroundRegistry(t *testing.T) {
	res := scanFixture(t)
	g := build(t, res, Options{Registry: "harbor.ground.local", Target: "crossplane"})
	var bundle string
	for _, a := range g.Artifacts {
		if a.Standalone {
			continue
		}
		bundle += a.YAML
	}
	for _, host := range []string{"ghcr.io/", "docker.io/", "quay.io/", "public.ecr.aws/"} {
		if strings.Contains(bundle, "image: "+host) || strings.Contains(bundle, "imageName: "+host) {
			t.Errorf("generated bundle still pulls from public registry %q", host)
		}
	}
	if !strings.Contains(bundle, "harbor.ground.local/versity/versitygw") {
		t.Error("object store image was not mirrored to the ground registry")
	}
}

func TestECRImagesAreRewritten(t *testing.T) {
	res := scanFixture(t)
	g := build(t, res, Options{Registry: "harbor.ground.local"})
	var bundle string
	for _, a := range g.Artifacts {
		bundle += a.YAML
	}
	if strings.Contains(bundle, "image: 123456789012.dkr.ecr") {
		t.Error("ECR image reference survived generation")
	}
	if !strings.Contains(bundle, "harbor.ground.local/") {
		t.Error("ground registry not used for rewritten images")
	}
}

func TestRewriteImage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"123456789012.dkr.ecr.us-west-2.amazonaws.com/turion/api:2.4.1",
			"reg.ground/turion/api:2.4.1"},
		{"public.ecr.aws/abc/svc:1", "reg.ground/abc/svc:1"},
		{"plain-image:1.0", "reg.ground/plain-image:1.0"},
		{"ghcr.io/versity/versitygw:v1", "ghcr.io/versity/versitygw:v1"},
		{"reg.ground/ground/x:1", "reg.ground/ground/x:1"},
	}
	for _, c := range cases {
		got, _ := rewriteImage(c.in, "reg.ground")
		if got != c.want {
			t.Errorf("rewriteImage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitWritesOneFilePerObject(t *testing.T) {
	res := scanFixture(t)
	dir := t.TempDir()
	g, err := Build(res, Options{OutDir: dir, Split: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Files) != len(g.Artifacts) {
		t.Fatalf("split mode wrote %d files for %d artifacts", len(g.Files), len(g.Artifacts))
	}
	for _, f := range g.Files {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("missing generated file %s", f)
		}
		name := filepath.Base(f)
		if !strings.HasSuffix(name, ".yaml") {
			t.Errorf("unexpected file name %s", name)
		}
	}
}

func TestBatchJobDefinitionIsMirrored(t *testing.T) {
	res := scanFixture(t)
	g := build(t, res, Options{})
	var bundle string
	for _, a := range g.Artifacts {
		bundle += a.YAML
	}
	if !strings.Contains(bundle, "name: droid-pass-processor") {
		t.Error("the AWS Batch job definition name was not mirrored into a Kubernetes Job")
	}
	// vcpus: 4 and memory 8192 should map to cpu 4 / memory 8Gi
	if !strings.Contains(bundle, `cpu: "4"`) {
		t.Error("Batch vcpus were not carried into the Job resource requests")
	}
}

func TestKustomizeOverlayIsStandalone(t *testing.T) {
	res := scanFixture(t)
	dir := t.TempDir()
	g, err := Build(res, Options{OutDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(dir, "ground-station-portable.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle), "kind: Kustomization") {
		t.Error("a kustomization cannot be applied by kubectl and must not be in the bundle")
	}
	overlay := filepath.Join(dir, "kustomize", "kustomization.yaml")
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("expected the kustomize overlay at %s: %v", overlay, err)
	}
	found := false
	for _, f := range g.Files {
		if f == overlay {
			found = true
		}
	}
	if !found {
		t.Error("the overlay path should be reported in Result.Files")
	}
}

func TestNoArtifactDirectoriesWhenOutDirEmpty(t *testing.T) {
	res := scanFixture(t)
	g, err := Build(res, Options{OutDir: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Files) != 0 {
		t.Fatalf("expected no files written, got %v", g.Files)
	}
	if len(g.Artifacts) == 0 {
		t.Fatal("expected in-memory artifacts")
	}
}
