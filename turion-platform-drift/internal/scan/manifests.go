package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// shared literal matchers
// ---------------------------------------------------------------------------

var (
	arnAWSRe  = regexp.MustCompile(`arn:aws[a-z-]*:([a-z0-9-]+):`)
	s3URIRe   = regexp.MustCompile(`s3://([A-Za-z0-9._-]+)`)
	ecrRe     = regexp.MustCompile(`([0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com|[a-z0-9.-]*\.dkr\.ecr\.amazonaws\.com)`)
	ebsCSIVRe = regexp.MustCompile(`(ebs\.csi\.aws\.com|efs\.csi\.aws\.com|\b(gp2|gp3|io1|io2|st1|sc1)\b)`)
)

func matchArn(s string) (string, bool) {
	m := arnAWSRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

func matchS3URI(s string) (string, bool) {
	m := s3URIRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

// ---------------------------------------------------------------------------
// generic text scanner for formats without a parser (TS/JS, HCL, shell)
// ---------------------------------------------------------------------------

var (
	tsAwsImportRe  = regexp.MustCompile(`from\s+['"](@aws-sdk/[^'"]+|aws-sdk|aws-amplify)['"]|require\(\s*['"](@aws-sdk/[^'"]+|aws-sdk)['"]\s*\)`)
	tsLambdaRe     = regexp.MustCompile(`\bnew\s+(LambdaClient|Lambda)\s*\(|\b(InvokeCommand)\s*\(`)
	tsS3Re         = regexp.MustCompile(`\bnew\s+(S3Client|S3)\s*\(|\b(PutObjectCommand|GetObjectCommand|DeleteObjectCommand)\s*\(`)
	tsClientRe     = regexp.MustCompile(`\bnew\s+(DynamoDBClient|SQSClient|SNSClient|BatchClient|SchedulerClient|CloudWatchClient)\s*\(`)
	hclAwsRe       = regexp.MustCompile(`^\s*(resource|data)\s+"(aws_[a-z0-9_]+)"`)
	cfnAwsRe       = regexp.MustCompile(`"Type"\s*:\s*"(AWS::[A-Za-z0-9:]+)"`)
	storageClassRe = regexp.MustCompile(`(?i)^storageClassName\s*:\s*["']?(gp2|gp3|io1|io2|st1|sc1)["']?\s*$`)
	awsCLIRe       = regexp.MustCompile(`^\s*(?:-\s*)?aws\s+([a-z0-9-]+)\s`)
)

// ScanText applies the appropriate text-level rules for lang.
func ScanText(rel, content string, lang Language, c *Collector) {
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		lineNo := i + 1

		// ARNs / S3 URIs / ECR references appear in every language and manifest.
		for _, m := range arnAWSRe.FindAllStringSubmatch(line, -1) {
			svc := m[1]
			rule := "TPD007"
			if svc == "iam" || svc == "sts" {
				rule = "TPD009"
			}
			c.Add(New(rule, rel, lineNo, lang, Truncate(line, 90), svc,
				"hard-codes an "+svc+" ARN literal"))
		}
		for _, m := range s3URIRe.FindAllStringSubmatch(line, -1) {
			c.Add(New("TPD005", rel, lineNo, lang, Truncate(line, 90), "s3",
				"hard-codes the S3 bucket '"+m[1]+"'"))
		}
		if lang != LangGo && lang != LangPython {
			if m := ecrRe.FindStringSubmatch(line); m != nil && !strings.Contains(line, "arn:aws") {
				c.Add(New("TPD042", rel, lineNo, lang, Truncate(line, 90), "ecr",
					"references an Amazon ECR image registry"))
			}
		}

		switch lang {
		case LangTypeScript:
			if m := tsAwsImportRe.FindStringSubmatch(line); m != nil {
				pkg := m[1]
				if pkg == "" {
					pkg = m[2]
				}
				c.Add(New("TPD030", rel, lineNo, lang, "import "+pkg, "aws-sdk",
					"imports the proprietary AWS SDK package "+pkg))
			}
			if tsLambdaRe.MatchString(line) {
				c.Add(New("TPD031", rel, lineNo, lang, Truncate(line, 70), "lambda",
					"invokes AWS Lambda from TypeScript/JavaScript"))
			}
			if tsS3Re.MatchString(line) {
				c.Add(New("TPD006", rel, lineNo, lang, Truncate(line, 70), "s3",
					"uses proprietary S3 SDK primitives"))
			}
			if tsClientRe.MatchString(line) {
				c.Add(New("TPD030", rel, lineNo, lang, Truncate(line, 70), "aws-sdk",
					"constructs an AWS SDK client"))
			}

		case LangHCL:
			if m := hclAwsRe.FindStringSubmatch(line); m != nil {
				c.Add(New("TPD049", rel, lineNo, lang, m[2], "terraform",
					"declares the AWS Terraform resource "+m[2]))
			}

		case LangShell:
			if m := awsCLIRe.FindStringSubmatch(line); m != nil {
				c.Add(New("TPD012", rel, lineNo, lang, Truncate(line, 70), "aws-cli",
					"calls `aws "+m[1]+"` from a script"))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Kubernetes / Helm / AWS Batch manifest linter
// ---------------------------------------------------------------------------

// ScanManifest lints one YAML or JSON manifest document.
func ScanManifest(rel, content string, lang Language, c *Collector) {
	lines := strings.Split(content, "\n")

	// JSON documents: parse so AWS Batch / CloudFormation structures are exact.
	if lang == LangJSON {
		scanJSONManifest(rel, content, c)
	}

	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(trimmed)

		// EKS IRSA: eks.amazonaws.com/role-arn
		if strings.Contains(lower, "eks.amazonaws.com/role-arn") {
			c.Add(New("TPD043", rel, lineNo, lang, "eks.amazonaws.com/role-arn", "iam",
				"binds a ServiceAccount to an IAM role via IRSA"))
		}
		// ECR image
		if m := ecrRe.FindStringSubmatch(trimmed); m != nil {
			c.Add(New("TPD042", rel, lineNo, lang, Truncate(trimmed, 90), "ecr",
				"pulls the image from Amazon ECR"))
		}
		// AWS Load Balancer Controller ingress
		if strings.Contains(lower, "kubernetes.io/ingress.class") && strings.Contains(lower, "alb") ||
			strings.Contains(lower, "alb.ingress.kubernetes.io/") ||
			strings.Contains(lower, "service.beta.kubernetes.io/aws-load-balancer") {
			c.Add(New("TPD044", rel, lineNo, lang, Truncate(trimmed, 90), "elb",
				"requires the AWS Load Balancer Controller"))
		}
		// AWS CSI storage
		if strings.Contains(lower, "ebs.csi.aws.com") || strings.Contains(lower, "efs.csi.aws.com") ||
			strings.Contains(lower, "fsx.csi.aws.com") {
			c.Add(New("TPD045", rel, lineNo, lang, Truncate(trimmed, 90), "ebs",
				"uses an AWS CSI volume provisioner"))
		}
		// storageClassName: gp3 (EBS)
		if m := storageClassRe.FindStringSubmatch(trimmed); m != nil && len(m) > 1 {
			c.Add(New("TPD045", rel, lineNo, lang, "storageClassName: "+m[1], "ebs",
				"requests the AWS EBS storage class '"+m[1]+"'"))
		}
		// Secrets Store CSI with an AWS provider
		if strings.Contains(lower, "secrets-store.csi.k8s.io") {
			c.Add(New("TPD046", rel, lineNo, lang, Truncate(trimmed, 90), "secretsmanager",
				"mounts secrets through the Secrets Store CSI driver (AWS provider)"))
		}
		if strings.Contains(lower, "awssecretsmanager") || strings.Contains(lower, "awssecretsmanagerproviderclass") ||
			strings.Contains(lower, "provider: aws") {
			c.Add(New("TPD046", rel, lineNo, lang, Truncate(trimmed, 90), "secretsmanager",
				"reads configuration from AWS Secrets Manager / SSM"))
		}
		// Karpenter EC2NodeClass
		if strings.Contains(lower, "kind: ec2nodeclass") || strings.Contains(lower, "ec2nodeclass") {
			c.Add(New("TPD047", rel, lineNo, lang, Truncate(trimmed, 90), "ec2",
				"provisions nodes through Karpenter's EC2NodeClass"))
		}
		// CloudWatch / X-Ray
		if strings.Contains(lower, "aws-observability") || strings.Contains(lower, "awsxray") ||
			strings.Contains(lower, "cloudwatch") && !strings.Contains(lower, "harbor") {
			c.Add(New("TPD048", rel, lineNo, lang, Truncate(trimmed, 90), "cloudwatch",
				"ships telemetry to CloudWatch/X-Ray"))
		}
		// CloudFormation / Terraform
		if m := cfnAwsRe.FindStringSubmatch(trimmed); m != nil {
			c.Add(New("TPD049", rel, lineNo, lang, m[1], "cloudformation",
				"declares the CloudFormation type "+m[1]))
		}
		// AWS Batch kwargs in values/params
		if strings.Contains(lower, "jobqueue") || strings.Contains(lower, "job_definition") ||
			strings.Contains(lower, "jobdefinition") || strings.Contains(lower, "computeenvironment") {
			c.Add(New("TPD004", rel, lineNo, lang, Truncate(trimmed, 90), "batch",
				"references an AWS Batch resource"))
		}
	}
}

func scanJSONManifest(rel, content string, c *Collector) {
	var doc any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		c.AddError("%s: JSON parse error: %v", rel, err)
		return
	}
	lineOf := func(needle string) int {
		for i, l := range strings.Split(content, "\n") {
			if strings.Contains(l, needle) {
				return i + 1
			}
		}
		return 1
	}

	switch d := doc.(type) {
	case map[string]any:
		// AWS Batch job definition
		if _, ok := d["jobDefinitionName"]; ok {
			c.Add(New("TPD040", rel, lineOf("jobDefinitionName"), LangJSON,
				"jobDefinitionName", "batch",
				"declares an AWS Batch job definition"))
		}
		if _, ok := d["containerProperties"]; ok {
			if _, isBatch := d["jobDefinitionName"]; isBatch {
				c.Add(New("TPD040", rel, lineOf("containerProperties"), LangJSON,
					"containerProperties", "batch",
					"defines the Batch container properties (vCPU/memory pins)"))
			}
		}
		// AWS Batch compute environment
		if _, ok := d["computeEnvironmentName"]; ok {
			c.Add(New("TPD041", rel, lineOf("computeEnvironmentName"), LangJSON,
				"computeEnvironmentName", "batch",
				"declares an AWS Batch compute environment"))
		}
		if _, ok := d["instanceTypes"]; ok {
			if _, isCE := d["computeEnvironmentName"]; isCE {
				c.Add(New("TPD041", rel, lineOf("instanceTypes"), LangJSON,
					"instanceTypes", "ec2",
					"pins EC2 instance types in a Batch compute environment"))
			}
		}
		// AWS Batch job queue
		if _, ok := d["jobQueueName"]; ok {
			c.Add(New("TPD004", rel, lineOf("jobQueueName"), LangJSON,
				"jobQueueName", "batch", "declares an AWS Batch job queue"))
		}
		// IAM policy document
		if _, ok := d["Statement"]; ok {
			if _, isPolicy := d["Version"]; isPolicy {
				c.Add(New("TPD009", rel, lineOf("Statement"), LangJSON,
					"Statement", "iam", "is a non-portable AWS IAM policy document"))
			}
		}
		// CloudFormation template
		if _, ok := d["Resources"]; ok {
			if _, isCFN := d["AWSTemplateFormatVersion"]; isCFN {
				c.Add(New("TPD049", rel, lineOf("Resources"), LangJSON,
					"AWSTemplateFormatVersion", "cloudformation",
					"is an AWS CloudFormation template"))
			}
		}
	case []any:
		for _, item := range d {
			if m, ok := item.(map[string]any); ok {
				if _, isBatch := m["jobDefinitionName"]; isBatch {
					c.Add(New("TPD040", rel, 1, LangJSON, "jobDefinitionName", "batch",
						"declares an AWS Batch job definition"))
				}
			}
		}
	}
}

// ScanAWSBatchYAML catches Batch manifests expressed in YAML.
func ScanAWSBatchYAML(rel, content string, c *Collector) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(lower, "jobdefinitionname:"):
			c.Add(New("TPD040", rel, i+1, LangYAML, "jobDefinitionName", "batch",
				"declares an AWS Batch job definition"))
		case strings.HasPrefix(lower, "computeenvironmentname:"):
			c.Add(New("TPD041", rel, i+1, LangYAML, "computeEnvironmentName", "batch",
				"declares an AWS Batch compute environment"))
		case strings.HasPrefix(lower, "jobqueuename:"):
			c.Add(New("TPD004", rel, i+1, LangYAML, "jobQueueName", "batch",
				"declares an AWS Batch job queue"))
		case strings.HasPrefix(lower, "type: container") && strings.Contains(content, "jobDefinitionName"):
			c.Add(New("TPD040", rel, i+1, LangYAML, "type: container", "batch",
				"Batch container job definition entry"))
		}
	}
}

// IsAWSPolicyPath reports whether a JSON path looks like an IAM policy file.
func IsAWSPolicyPath(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(p)
	if !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.Contains(p, "policy") || strings.Contains(p, "iam") ||
		strings.Contains(base, "trust")
}

// MaxReadBytes bounds a single file read. The walker already skips oversized
// files, but ReadFile is also called directly (job definitions, manifests), so
// it enforces its own ceiling rather than trusting the caller.
const MaxReadBytes = 8 << 20

// ReadFile reads a file, refusing anything absurdly large.
func ReadFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Size() > MaxReadBytes {
		return "", fmt.Errorf("%s is %d bytes, over the %d byte limit", path, fi.Size(), MaxReadBytes)
	}
	b, err := os.ReadFile(path)
	return string(b), err
}
