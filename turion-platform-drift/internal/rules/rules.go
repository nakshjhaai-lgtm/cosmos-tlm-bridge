// Package rules is the catalog of AWS lock-in detections and, for each one,
// the portable replacement turion-platform-drift can generate.
//
// Rule IDs are stable and appear verbatim in the scorecard, SARIF, and CI
// annotations, so a suppression in the ground-service repo survives refactors.
package rules

// Category groups a rule by the ground-segment capability it couples to AWS.
type Category string

const (
	CatCompute       Category = "compute"
	CatServerless    Category = "serverless"
	CatStorage       Category = "object-storage"
	CatIdentity      Category = "identity"
	CatMessaging     Category = "messaging"
	CatDatabase      Category = "database"
	CatNetwork       Category = "network"
	CatObservability Category = "observability"
	CatRegistry      Category = "registry"
	CatConfig        Category = "config"
)

// Severity ranks how fatal the coupling is for a portable ground station.
type Severity string

const (
	SeverityCritical Severity = "critical" // cannot run at all off-AWS
	SeverityHigh     Severity = "high"     // needs an AWS API at runtime
	SeverityMedium   Severity = "medium"   // portable with configuration
	SeverityLow      Severity = "low"      // advisory
)

// Rule describes one detection.
type Rule struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	Description string   `json:"description"`
	// Remediation is the concrete portable replacement.
	Remediation string `json:"remediation"`
	// Portability is the target abstraction the generator emits.
	Portability string `json:"portability"`
	// Docs links the AWS service being removed.
	Docs string `json:"docs,omitempty"`
}

// Catalog is every rule, keyed by ID.
var Catalog = map[string]Rule{
	// ---------------------------------------------------------------- Python
	"TPD001": {
		ID: "TPD001", Title: "boto3 / botocore SDK import",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "Ground-service code imports the proprietary AWS SDK directly, so the module cannot be imported on a non-AWS ground station.",
		Remediation: "Replace with the portable ground-station storage interface (any S3-compatible endpoint: MinIO, VersityGW) and read ARNs from environment configuration.",
		Portability: "OpenC3-compatible object-store shim + typed config",
	},
	"TPD002": {
		ID: "TPD002", Title: "boto3 client bound to a proprietary AWS service",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "A boto3 client is constructed for an AWS-only service (batch, lambda, s3, dynamodb, sqs, sns, ...), hard-coding the proprietary control plane.",
		Remediation: "Route the call through the portability interface; Batch becomes a Kubernetes Job, Lambda becomes a Deployment/Job, DynamoDB becomes PostgreSQL/Postgres-compatible, SQS/SNS becomes NATS/Redpanda.",
		Portability: "Kubernetes Job / Crossplane composite",
	},
	"TPD003": {
		ID: "TPD003", Title: "AWS Lambda invocation",
		Category: CatServerless, Severity: SeverityCritical,
		Description: "Code invokes a Lambda function, which cannot exist in an air-gapped or on-premises ground station.",
		Remediation: "Deploy the function body as a Kubernetes Job/Deployment and call it over HTTP (or the Knative Serving route) instead of Invoke.",
		Portability: "Kubernetes Job + ClusterIP Service",
	},
	"TPD004": {
		ID: "TPD004", Title: "AWS Batch job submission",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "Code or manifests submit to AWS Batch (submit_job, jobQueue, jobDefinition), which has no portable equivalent unless re-hosted.",
		Remediation: "Generate a Kubernetes Job (with backoffLimit and ttlSecondsAfterFinished) from the Batch job definition.",
		Portability: "Kubernetes Job manifest",
	},
	"TPD005": {
		ID: "TPD005", Title: "Hard-coded S3 bucket / ARN literal",
		Category: CatStorage, Severity: SeverityHigh,
		Description: "A bucket name or arn:aws:s3 ARN is embedded in source, binding the deployment to one AWS account and region.",
		Remediation: "Read the endpoint, bucket, and credentials from a ConfigMap/Secret; point them at MinIO or VersityGW.",
		Portability: "MinIO / VersityGW bucket + ConfigMap",
	},
	"TPD006": {
		ID: "TPD006", Title: "Proprietary S3 primitive usage",
		Category: CatStorage, Severity: SeverityHigh,
		Description: "Object access uses boto3 S3 primitives (get_object, put_object, list_objects_v2, generate_presigned_url) rather than an S3-compatible abstraction.",
		Remediation: "Wrap in the object-store interface; the S3 API is portable, the SDK is not.",
		Portability: "S3-compatible client factory",
	},
	"TPD007": {
		ID: "TPD007", Title: "arn:aws literal in source",
		Category: CatIdentity, Severity: SeverityCritical,
		Description: "A literal arn:aws: ARN is embedded, fixing the resource to one AWS partition/account/region.",
		Remediation: "Externalise into configuration and use Crossplane-provisioned resources with generated names.",
		Portability: "Crossplane composite + ConfigMap",
	},
	"TPD008": {
		ID: "TPD008", Title: "AWS IAM / STS role assumption",
		Category: CatIdentity, Severity: SeverityCritical,
		Description: "Code assumes an IAM role via STS, which depends on the AWS identity control plane.",
		Remediation: "Use workload identity from Kubernetes ServiceAccount (SPIFFE/IRSA-equivalent) or a Vault-injected token.",
		Portability: "Kubernetes ServiceAccount + Workload Identity",
	},
	"TPD009": {
		ID: "TPD009", Title: "Non-portable IAM policy document",
		Category: CatIdentity, Severity: SeverityCritical,
		Description: "An IAM policy document grants access using AWS ARNs or aws: source conditions, which no on-premises cluster can evaluate.",
		Remediation: "Replace with Kubernetes RBAC and an S3-compatible access key scoped to a bucket.",
		Portability: "RBAC Role/RoleBinding + MinIO policy",
	},
	"TPD010": {
		ID: "TPD010", Title: "AWS messaging service usage",
		Category: CatMessaging, Severity: SeverityHigh,
		Description: "Code publishes or consumes via SQS/SNS/Kinesis/EventBridge, proprietary managed brokers.",
		Remediation: "Target NATS JetStream or Redpanda, both of which run air-gapped.",
		Portability: "NATS JetStream / Redpanda",
	},
	"TPD011": {
		ID: "TPD011", Title: "AWS managed database usage",
		Category: CatDatabase, Severity: SeverityHigh,
		Description: "Code targets DynamoDB/RDS/Aurora control-plane APIs rather than a portable wire protocol.",
		Remediation: "Use PostgreSQL (CloudNativePG) or an S3-backed open table format.",
		Portability: "CloudNativePG PostgreSQL cluster",
	},
	"TPD012": {
		ID: "TPD012", Title: "AWS CLI invocation of a proprietary service",
		Category: CatCompute, Severity: SeverityHigh,
		Description: "A shell-out to the aws CLI targets an AWS control plane at deploy or run time.",
		Remediation: "Replace with kubectl/Helm or Crossplane; the ground station must not require AWS credentials.",
		Portability: "kubectl / Helm / Crossplane",
	},

	// ------------------------------------------------------------------- Go
	"TPD020": {
		ID: "TPD020", Title: "aws-sdk-go / aws-sdk-go-v2 import",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "A Go module imports the AWS SDK, linking the binary to the AWS control plane.",
		Remediation: "Swap to the portable interface; keep an S3-compatible client backed by configuration.",
		Portability: "S3-compatible client factory",
	},
	"TPD021": {
		ID: "TPD021", Title: "AWS SDK service client in Go",
		Category: CatServerless, Severity: SeverityCritical,
		Description: "Go code constructs an AWS service client (lambda, batch, s3, dynamodb, sqs, sns, sts).",
		Remediation: "Repoint at the portable replacement generated by this tool.",
		Portability: "Kubernetes Job / object-store shim",
	},

	// --------------------------------------------------------- TypeScript
	"TPD030": {
		ID: "TPD030", Title: "@aws-sdk/* package import",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "A TypeScript/JavaScript module imports an AWS SDK package that only resolves against AWS endpoints.",
		Remediation: "Use an S3-compatible client (for example the S3 API via a configured endpoint) and call ground microservices over HTTP.",
		Portability: "S3-compatible client + HTTP",
	},
	"TPD031": {
		ID: "TPD031", Title: "AWS SDK Lambda client usage",
		Category: CatServerless, Severity: SeverityCritical,
		Description: "TypeScript/JavaScript invokes Lambda directly from the ground UI or BFF.",
		Remediation: "Call the generated Kubernetes Service instead of InvokeCommand.",
		Portability: "Kubernetes Service",
	},

	// ------------------------------------------------------- manifests / IaC
	"TPD040": {
		ID: "TPD040", Title: "AWS Batch job definition manifest",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "An AWS Batch jobDefinition JSON document defines the workload in a proprietary scheduler.",
		Remediation: "Generate an equivalent Kubernetes Job (backoffLimit, ttlSecondsAfterFinished, resources).",
		Portability: "Kubernetes Job manifest",
	},
	"TPD041": {
		ID: "TPD041", Title: "AWS Batch compute environment manifest",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "An AWS Batch compute environment pins the workload to EC2/EKS managed by AWS Batch.",
		Remediation: "Use a Kubernetes node pool or Karpenter NodePool plus a PriorityClass.",
		Portability: "NodePool / PriorityClass",
	},
	"TPD042": {
		ID: "TPD042", Title: "Amazon ECR image reference",
		Category: CatRegistry, Severity: SeverityCritical,
		Description: "A container image comes from *.dkr.ecr.*.amazonaws.com, which an air-gapped registry cannot reach.",
		Remediation: "Mirror into the ground-station registry (Harbor/Zot) and reference it by local name.",
		Portability: "Harbor / Zot registry",
	},
	"TPD043": {
		ID: "TPD043", Title: "IRSA annotation (eks.amazonaws.com/role-arn)",
		Category: CatIdentity, Severity: SeverityCritical,
		Description: "A ServiceAccount is bound to an IAM role via IRSA, which only the EKS control plane can interpret.",
		Remediation: "Use a Kubernetes ServiceAccount with an S3-compatible access key Secret, or SPIFFE workload identity.",
		Portability: "ServiceAccount + Secret",
	},
	"TPD044": {
		ID: "TPD044", Title: "AWS Load Balancer Controller ingress",
		Category: CatNetwork, Severity: SeverityHigh,
		Description: "Ingress depends on the AWS Load Balancer Controller (alb scheme/annotations), absent off-AWS.",
		Remediation: "Use a portable ingress (NGINX or Gateway API) with no cloud-specific annotations.",
		Portability: "NGINX Ingress / Gateway API",
	},
	"TPD045": {
		ID: "TPD045", Title: "AWS EBS/EFS storage class",
		Category: CatStorage, Severity: SeverityHigh,
		Description: "A PersistentVolumeClaim uses an AWS CSI provisioner (ebs.csi.aws.com, efs.csi.aws.com, gp2/gp3).",
		Remediation: "Use the ground station's portable storage class (local-path, Longhorn, Ceph RBD).",
		Portability: "Longhorn / Ceph RBD storage class",
	},
	"TPD046": {
		ID: "TPD046", Title: "AWS Secrets Manager / SSM CSI volume",
		Category: CatConfig, Severity: SeverityCritical,
		Description: "Secrets are mounted from AWS Secrets Manager or SSM Parameter Store via the Secrets Store CSI driver.",
		Remediation: "Mount Kubernetes Secrets, optionally synced by External Secrets Operator from Vault.",
		Portability: "Kubernetes Secret / External Secrets + Vault",
	},
	"TPD047": {
		ID: "TPD047", Title: "Karpenter EC2NodeClass",
		Category: CatCompute, Severity: SeverityHigh,
		Description: "Karpenter is configured against EC2 via an EC2NodeClass, coupling provisioning to AWS.",
		Remediation: "Use a portable NodePool/Cluster Autoscaler for the ground-station cluster.",
		Portability: "NodePool + Cluster Autoscaler",
	},
	"TPD048": {
		ID: "TPD048", Title: "CloudWatch Logs / X-Ray dependency",
		Category: CatObservability, Severity: SeverityHigh,
		Description: "Telemetry and logs are shipped to CloudWatch or X-Ray, which cannot be reached from an air gap.",
		Remediation: "Ship OTLP to a local OpenTelemetry Collector exported to Prometheus/Loki/Tempo.",
		Portability: "OpenTelemetry Collector",
	},
	"TPD049": {
		ID: "TPD049", Title: "aws_* Terraform / CloudFormation resource",
		Category: CatCompute, Severity: SeverityCritical,
		Description: "Infrastructure is declared with aws_* Terraform resources or AWS:: CloudFormation types.",
		Remediation: "Express the same resource as a Crossplane composite (portable) or plain Kubernetes objects.",
		Portability: "Crossplane composite resource",
	},
}

// All returns every rule, ordered by ID.
func All() []Rule {
	out := make([]Rule, 0, len(Catalog))
	for _, r := range Catalog {
		out = append(out, r)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Get returns a rule by ID.
func Get(id string) (Rule, bool) {
	r, ok := Catalog[id]
	return r, ok
}

// Weight is the score penalty a finding of this severity incurs (0-100 scale).
func (r Rule) Weight() float64 {
	switch r.Severity {
	case SeverityCritical:
		return 12
	case SeverityHigh:
		return 7
	case SeverityMedium:
		return 3
	default:
		return 1
	}
}
