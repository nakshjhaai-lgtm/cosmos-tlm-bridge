// Package generate produces portable replacements for the AWS dependencies a
// scan found: Kubernetes Jobs in place of AWS Batch, MinIO/VersityGW object
// stores in place of S3, HTTP Services in place of Lambda, Crossplane
// composites for provisioning, and an OpenTelemetry Collector in place of
// CloudWatch/X-Ray.
package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"turion-platform-drift/internal/rules"
	"turion-platform-drift/internal/scan"
)

// Options controls generation.
type Options struct {
	// OutDir is where manifests are written.
	OutDir string
	// Namespace is the target ground-station namespace.
	Namespace string
	// Target names the deployment target: "kubernetes" or "crossplane".
	Target string
	// Registry is the ground-station registry prefix (Harbor/Zot).
	Registry string
	// ObjectStore is "minio" or "versitygw".
	ObjectStore string
	// Split writes one file per object instead of a single bundle.
	Split bool
}

func (o *Options) defaults() {
	if o.Namespace == "" {
		o.Namespace = "ground-system"
	}
	if o.Target == "" {
		o.Target = "kubernetes"
	}
	if o.Registry == "" {
		o.Registry = "registry.ground.local"
	}
	if o.ObjectStore == "" {
		o.ObjectStore = "versitygw"
	}
}

// Artifact is one generated manifest.
type Artifact struct {
	Name string
	YAML string
	// Standalone artifacts are written to their own file and are NOT part of
	// the applied bundle (a kustomize overlay is not a cluster object).
	Standalone bool
}

// Result is the generator output.
type Result struct {
	Artifacts []Artifact
	Files     []string
	Notes     []string
}

// Build derives the replacement manifests from a scan result.
func Build(res *scan.Result, opts Options) (*Result, error) {
	opts.defaults()
	out := &Result{}
	seenBucket := map[string]bool{}
	seenJob := map[string]bool{}

	// The namespace must be first so one `kubectl apply -f` pass works.
	out.Artifacts = append(out.Artifacts, Artifact{
		Name: "k8s-namespace", YAML: namespaceObject(opts),
	})

	// Group findings by the capability they need.
	byCategory := map[rules.Category][]scan.Finding{}
	for _, f := range res.Findings {
		byCategory[f.Category] = append(byCategory[f.Category], f)
	}

	// ---- compute: AWS Batch -> Kubernetes Job -------------------------
	if fs := byCategory[rules.CatCompute]; len(fs) > 0 {
		jobs := batchJobCandidates(res.Root, fs)
		for _, j := range jobs {
			if seenJob[j.name] {
				continue
			}
			seenJob[j.name] = true
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "k8s-job-" + j.name,
				YAML: kubernetesJob(j.name, opts, j),
			})
		}
		if len(jobs) == 0 {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "k8s-job-ground-etl",
				YAML: kubernetesJob("ground-etl", opts, batchCandidate{
					name: "ground-etl", source: "generated", image: opts.Registry + "/ground-service:1.0.0",
				}),
			})
		}
	}

	// ---- serverless: Lambda -> Deployment + Service -------------------
	if fs := byCategory[rules.CatServerless]; len(fs) > 0 {
		name := "telemetry-enrich"
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "k8s-deployment-" + name,
			YAML: kubernetesDeploymentService(name, opts),
		})
	}

	// ---- object storage: S3 -> VersityGW / MinIO ----------------------
	storeArtifacts, buckets := objectStore(opts, byCategory[rules.CatStorage])
	out.Artifacts = append(out.Artifacts, storeArtifacts...)
	if opts.Target == "crossplane" {
		// Managed resources: one Bucket CR per bucket (provider-minio).
		for _, b := range buckets {
			if seenBucket[b] {
				continue
			}
			seenBucket[b] = true
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "crossplane-bucket-" + b,
				YAML: objectBucket(b, opts),
			})
		}
	} else {
		// Built-in kinds only: ConfigMap inventory + bootstrap Job.
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "k8s-bucket-bootstrap",
			YAML: bucketBootstrap(buckets, opts),
		})
	}

	// ---- identity: IAM/IRSA -> ServiceAccount + Secret + RBAC ---------
	if _, ok := byCategory[rules.CatIdentity]; ok {
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "k8s-serviceaccount-ground-service",
			YAML: serviceAccount(opts),
		})
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "k8s-rbac-ground-service",
			YAML: rbac(opts),
		})
	}

	// ---- messaging: SQS/SNS -> NATS JetStream ------------------------
	if _, ok := byCategory[rules.CatMessaging]; ok {
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "k8s-nats-jetstream",
			YAML: natsJetStream(opts),
		})
	}

	// ---- database: DynamoDB/RDS -> CloudNativePG ----------------------
	if _, ok := byCategory[rules.CatDatabase]; ok {
		if opts.Target == "crossplane" {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "crossplane-postgres-cluster",
				YAML: postgresCluster(opts),
			})
		} else {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "k8s-postgres-statefulset",
				YAML: postgresPortable(opts),
			})
		}
	}

	// ---- observability: CloudWatch -> OpenTelemetry Collector ---------
	if _, ok := byCategory[rules.CatObservability]; ok || len(res.Findings) > 0 {
		if opts.Target == "crossplane" {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "otel-collector",
				YAML: otelCollector(opts),
			})
		} else {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "k8s-otel-collector",
				YAML: otelPortable(opts),
			})
		}
	}

	// ---- config: Secrets Manager -> ExternalSecret --------------------
	if _, ok := byCategory[rules.CatConfig]; ok {
		if opts.Target == "crossplane" {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "k8s-externalsecret-ground-service",
				YAML: externalSecret(opts),
			})
		} else {
			out.Artifacts = append(out.Artifacts, Artifact{
				Name: "k8s-secret-ground-service",
				YAML: secretPlaceholder(opts),
			})
		}
	}

	// ---- registry: ECR -> Harbor/Zot reference -----------------------
	if _, ok := byCategory[rules.CatRegistry]; ok {
		out.Artifacts = append(out.Artifacts, Artifact{
			Name:       "kustomize",
			YAML:       registryRewrite(opts),
			Standalone: true,
		})
	}

	// ---- Crossplane composition (always useful for provisioning) ------
	if opts.Target == "crossplane" {
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "crossplane-composition-groundstack",
			YAML: crossplaneComposition(opts),
		})
		out.Artifacts = append(out.Artifacts, Artifact{
			Name: "crossplane-xrd-groundstack",
			YAML: crossplaneXRD(opts),
		})
	}

	// An air-gapped bundle must never depend on a public registry. The
	// kustomize overlay is intentionally excluded: it documents the original
	// ECR names that operators mirror into the ground registry.
	localizeExternalImages(out.Artifacts, opts.Registry)

	// ---- written files -------------------------------------------------
	if err := write(out, res, opts); err != nil {
		return nil, err
	}
	return out, nil
}

type batchCandidate struct {
	name     string
	source   string
	image    string
	vCPU     int
	memoryGi int
	queue    string
}

// manifestStems are filenames that describe a Batch resource rather than a
// workload; they are handled by reading the manifest, not by slugging.
var manifestStems = map[string]bool{
	"job-definition": true, "jobdefinition": true, "compute-environment": true,
	"computeenvironment": true, "job-queue": true, "jobqueue": true,
	"main": true, "index": true, "values": true, "chart": true,
}

// batchJobCandidates derives Kubernetes Job specs from the findings that imply
// Batch work. Where an AWS Batch job definition exists, its real name, image,
// vCPU and memory are carried across so the portable Job is drop-in.
func batchJobCandidates(root string, fs []scan.Finding) []batchCandidate {
	seen := map[string]batchCandidate{}
	manifestFiles := map[string]bool{}

	for _, f := range fs {
		stem := slug(strings.TrimSuffix(filepath.Base(f.File), filepath.Ext(f.File)))
		if manifestStems[stem] {
			manifestFiles[f.File] = true
			continue
		}
		if f.RuleID != "TPD004" && f.RuleID != "TPD020" && f.RuleID != "TPD021" {
			continue
		}
		if stem == "" {
			stem = slug(filepath.Base(filepath.Dir(f.File)))
		}
		if stem == "" || stem == "." {
			stem = "ground-etl"
		}
		if seen[stem].name != "" {
			continue
		}
		seen[stem] = batchCandidate{
			name: stem, source: f.File, image: "ground-service:1.0.0",
			vCPU: 2, memoryGi: 4, queue: "ground-processing",
		}
	}

	// Read every AWS Batch job definition we saw and mirror it exactly.
	for file := range manifestFiles {
		c, ok := readBatchJobDefinition(filepath.Join(root, file), file)
		if !ok {
			continue
		}
		if seen[c.name].name != "" {
			c.name = c.name + "-job"
		}
		seen[c.name] = c
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]batchCandidate, 0, len(names))
	for _, n := range names {
		out = append(out, seen[n])
	}
	return out
}

// readBatchJobDefinition extracts the portable Job parameters from an AWS
// Batch job definition manifest (JSON or YAML).
func readBatchJobDefinition(path, rel string) (batchCandidate, bool) {
	content, err := scan.ReadFile(path)
	if err != nil {
		return batchCandidate{}, false
	}
	c := batchCandidate{source: rel, vCPU: 2, memoryGi: 4, queue: "ground-processing"}

	getString := func(key string) string {
		for _, line := range strings.Split(content, "\n") {
			t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
			if !strings.HasPrefix(t, "\""+key+"\"") && !strings.HasPrefix(t, key+":") {
				continue
			}
			if i := strings.Index(t, ":"); i > 0 {
				v := strings.TrimSpace(t[i+1:])
				v = strings.TrimPrefix(v, "\"")
				v = strings.TrimSuffix(v, "\"")
				v = strings.TrimSuffix(v, ",")
				if v != "" {
					return v
				}
			}
		}
		return ""
	}

	name := getString("jobDefinitionName")
	if name == "" {
		return batchCandidate{}, false
	}
	c.name = slug(name)
	c.image = getString("image")

	if v := getString("vcpus"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.vCPU = n
		}
	}
	if m := getString("memory"); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			c.memoryGi = (n + 1023) / 1024
		}
	}
	return c, true
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// bucketCandidates extracts bucket names from findings.
func bucketCandidates(fs []scan.Finding) []string {
	seen := map[string]bool{}
	for _, f := range fs {
		sym := f.Symbol
		if strings.HasPrefix(sym, "s3://") {
			seen[slug(strings.TrimPrefix(sym, "s3://"))] = true
			continue
		}
		if f.RuleID == "TPD005" && sym != "" && !strings.Contains(sym, "arn:") {
			if strings.Contains(sym, "'") {
				parts := strings.Split(sym, "'")
				if len(parts) > 1 {
					seen[slug(parts[1])] = true
					continue
				}
			}
			seen[slug(sym)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		if b == "" || len(b) < 3 {
			continue
		}
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

func objectStore(opts Options, fs []scan.Finding) ([]Artifact, []string) {
	buckets := bucketCandidates(fs)
	if buckets == nil {
		buckets = []string{}
	}
	if len(buckets) == 0 {
		buckets = []string{"tlm-archive"}
	}
	return []Artifact{{Name: "k8s-objectstore-" + opts.ObjectStore, YAML: objectStoreDeployment(opts, buckets)}}, buckets
}

func write(out *Result, res *scan.Result, opts Options) error {
	if opts.OutDir == "" {
		return nil
	}
	dir := opts.OutDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	header := func(name string) string {
		return fmt.Sprintf(
			"# Generated by turion-platform-drift from %s\n"+
				"# Portable replacement artifact: %s\n"+
				"# Namespace: %s  Target: %s  Object store: %s\n"+
				"# Validate:  kubectl apply --dry-run=client -f .\n"+
				"# Air gap:   turion-platform-drift validate .\n",
			res.Root, name, opts.Namespace, opts.Target, opts.ObjectStore)
	}

	if opts.Split {
		for _, a := range out.Artifacts {
			p := filepath.Join(dir, a.Name+".yaml")
			if err := os.WriteFile(p, []byte(header(a.Name)+a.YAML), 0o644); err != nil {
				return err
			}
			out.Files = append(out.Files, p)
		}
		return nil
	}

	// Apply order matters for a single-pass `kubectl apply -f`: namespaces and
	// configuration first, then identity, storage, services and workloads.
	ordered := append([]Artifact(nil), out.Artifacts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return applyRank(ordered[i]) < applyRank(ordered[j])
	})

	var bundle strings.Builder
	bundle.WriteString(header("bundle"))
	first := true
	for _, a := range ordered {
		if a.Standalone {
			// A kustomize overlay is not a cluster object: write it next to the
			// bundle so `kubectl apply -f <dir>` never tries to apply it.
			p := filepath.Join(dir, a.Name, "kustomization.yaml")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(a.YAML), 0o644); err != nil {
				return err
			}
			out.Files = append(out.Files, p)
			continue
		}
		if !first {
			bundle.WriteString("\n---\n")
		}
		first = false
		bundle.WriteString(a.YAML)
		if !strings.HasSuffix(a.YAML, "\n") {
			bundle.WriteString("\n")
		}
	}
	p := filepath.Join(dir, "ground-station-portable.yaml")
	if err := os.WriteFile(p, []byte(bundle.String()), 0o644); err != nil {
		return err
	}
	out.Files = append(out.Files, p)
	return nil
}

// localizeExternalImages rewrites public-registry image references to the
// configured ground-station registry. Generated manifests are designed to be
// deployable with no internet egress, so ghcr.io, docker.io, quay.io and
// public.ecr.aws are offline-unsafe even though they are not AWS SDK calls.
func localizeExternalImages(artifacts []Artifact, registry string) {
	registry = strings.TrimSuffix(registry, "/")
	hosts := []string{"ghcr.io/", "docker.io/", "quay.io/", "public.ecr.aws/"}
	for i := range artifacts {
		if artifacts[i].Standalone {
			continue
		}
		y := artifacts[i].YAML
		for _, host := range hosts {
			y = strings.ReplaceAll(y, "image: "+host, "image: "+registry+"/")
			y = strings.ReplaceAll(y, "imageName: "+host, "imageName: "+registry+"/")
		}
		artifacts[i].YAML = y
	}
}

// applyRank orders objects the way a single-pass apply needs them.
func applyRank(a Artifact) int {
	if a.Standalone {
		return 1000
	}
	kinds := map[string]int{
		"Namespace": 0,
		"ConfigMap": 10, "Secret": 11, "ServiceAccount": 12,
		"Role": 20, "RoleBinding": 21, "ClusterRole": 20, "ClusterRoleBinding": 21,
		"PersistentVolumeClaim": 30,
		"Service":               40,
		"Bucket":                50, "SecretStore": 50, "Cluster": 50, "OpenTelemetryCollector": 50,
		"Deployment": 60, "StatefulSet": 61, "Job": 62,
		"ExternalSecret": 70, "Kustomization": 80,
		"CompositeResourceDefinition": 90, "Composition": 91,
	}
	best := 500
	for _, line := range strings.Split(a.YAML, "\n") {
		if !strings.HasPrefix(line, "kind: ") {
			continue
		}
		k := strings.TrimSpace(strings.TrimPrefix(line, "kind: "))
		if r, ok := kinds[k]; ok && r < best {
			best = r
		}
	}
	return best
}

// Manifest is the minimal shape the validator needs.
type Manifest struct {
	APIVersion string         `yaml:"apiVersion" json:"apiVersion"`
	Kind       string         `yaml:"kind" json:"kind"`
	Metadata   map[string]any `yaml:"metadata" json:"metadata"`
}

// asJSON is a debugging helper for generated docs.
func asJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
