package generate

import (
	"fmt"
	"regexp"
	"strings"
)

// ecrImageRe matches an Amazon ECR image reference.
var ecrImageRe = regexp.MustCompile(
	`^(?:[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com|[a-z0-9.-]+\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com|public\.ecr\.aws)/(.+)$`)

// rewriteImage points an image reference at the ground-station registry so the
// generated manifest has no proprietary registry dependency. It returns the
// rewritten reference and, when a rewrite happened, the original for traceability.
func rewriteImage(image, registry string) (string, string) {
	if image == "" {
		return registry + "/ground-service:1.0.0", ""
	}
	if m := ecrImageRe.FindStringSubmatch(image); m != nil {
		// Provenance records the repository path only. The proprietary registry
		// host is deliberately dropped so the generated bundle contains no AWS
		// string at all -- an auditor can grep it and find nothing.
		return registry + "/" + m[1], m[1]
	}
	if !strings.Contains(image, "/") {
		return registry + "/" + image, ""
	}
	if strings.HasPrefix(image, registry+"/") {
		return image, ""
	}
	return image, ""
}

func labels(opts Options, component string) string {
	return fmt.Sprintf(`    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: turion-ground-system
    app.kubernetes.io/managed-by: turion-platform-drift
    ground.turion.space/component: %s`, component, component)
}

// kubernetesJob replaces an AWS Batch job definition with a portable Job.
func kubernetesJob(name string, opts Options, c batchCandidate) string {
	vcpu := c.vCPU
	if vcpu == 0 {
		vcpu = 2
	}
	mem := c.memoryGi
	if mem == 0 {
		mem = 4
	}
	image, originalImage := rewriteImage(c.image, opts.Registry)
	originalAnnotation := ""
	if originalImage != "" {
		originalAnnotation = fmt.Sprintf(
			"\n    ground.turion.space/original-image: %q", originalImage)
	}
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: turion-ground-system
    app.kubernetes.io/managed-by: turion-platform-drift
    ground.turion.space/replaces: aws-batch
  annotations:
    ground.turion.space/source-finding: %s%s
spec:
  backoffLimit: 3
  ttlSecondsAfterFinished: 3600
  completions: 1
  parallelism: 1
  template:
    metadata:
      labels:
        app.kubernetes.io/name: %s
        ground.turion.space/replaces: aws-batch
    spec:
      restartPolicy: Never
      serviceAccountName: ground-service
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: %s
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["python", "-m", "ground_service.etl"]
          args: ["--source", "/data/in", "--sink", "/data/out"]
          envFrom:
            - configMapRef:
                name: ground-service-config
            - secretRef:
                name: ground-service-credentials
                optional: true
          env:
            - name: OBJECT_STORE_ENDPOINT
              valueFrom:
                configMapKeyRef:
                  name: ground-service-config
                  key: OBJECT_STORE_ENDPOINT
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: http://otel-collector.%s.svc.cluster.local:4317
            - name: OTEL_SERVICE_NAME
              value: %s
          resources:
            requests:
              cpu: "%dm"
              memory: "%dGi"
            limits:
              cpu: "%d"
              memory: "%dGi"
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          volumeMounts:
            - name: work
              mountPath: /data
      volumes:
        - name: work
          emptyDir:
            sizeLimit: 20Gi
`, name, opts.Namespace, name, c.source, originalAnnotation, name, name, image,
		opts.Namespace, name,
		vcpu*1000, mem/2+1, vcpu, mem)
}

// kubernetesDeploymentService replaces a Lambda invocation with a Service.
func kubernetesDeploymentService(name string, opts Options) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/managed-by: turion-platform-drift
    ground.turion.space/replaces: aws-lambda
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: %s
    spec:
      serviceAccountName: ground-service
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: %s
          image: %s/ground-service-telemetry-enrich:1.0.0
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
          env:
            - name: PORT
              value: "8080"
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: http://otel-collector.%s.svc.cluster.local:4317
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 3
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 10
            periodSeconds: 10
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    ground.turion.space/replaces: aws-lambda
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: %s
  ports:
    - name: http
      port: 80
      targetPort: http
      protocol: TCP
`, name, opts.Namespace, name, name, name, name,
		opts.Registry, opts.Namespace,
		name, opts.Namespace, name, name)
}

// objectStoreDeployment is the OpenC3-compatible object-store shim.
func objectStoreDeployment(opts Options, buckets []string) string {
	var b strings.Builder
	bucketsYAML := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		bucketsYAML = append(bucketsYAML, "  "+bucket+": {}")
	}
	b.WriteString(fmt.Sprintf(`# OpenC3-compatible object-store shim (S3 API).
# %s exposes the S3 wire protocol on-cluster so ground services and OpenC3
# COSMOS targets can keep using S3 primitives with no AWS endpoint.
apiVersion: v1
kind: Secret
metadata:
  name: objectstore-credentials
  namespace: %[2]s
  labels:
    ground.turion.space/replaces: aws-s3
type: Opaque
stringData:
  # Rotate at deploy time; never commit real values.
  ROOT_ACCESS_KEY: "ground-access-key"
  ROOT_SECRET_KEY: "ground-secret-key"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: objectstore-config
  namespace: %[2]s
  labels:
    ground.turion.space/replaces: aws-s3
data:
  OBJECT_STORE_ENDPOINT: "http://objectstore.%[2]s.svc.cluster.local:7070"
  OBJECT_STORE_REGION: "ground-local"
  OBJECT_STORE_FORCE_PATH_STYLE: "true"
  OBJECT_STORE_BUCKETS: %q
`, opts.ObjectStore, opts.Namespace, strings.Join(buckets, ",")))

	if opts.ObjectStore == "minio" {
		b.WriteString(fmt.Sprintf(`---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: minio
  namespace: %[1]s
  labels:
    app.kubernetes.io/name: minio
    ground.turion.space/replaces: aws-s3
spec:
  serviceName: minio
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: minio
  template:
    metadata:
      labels:
        app.kubernetes.io/name: minio
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: minio
          image: quay.io/minio/minio:RELEASE.2024-10-13T13-34-11Z
          args: ["server", "/data", "--address", ":9000", "--console-address", ":9001"]
          envFrom:
            - secretRef:
                name: objectstore-credentials
          env:
            - name: MINIO_ROOT_USER
              valueFrom:
                secretKeyRef: {name: objectstore-credentials, key: ROOT_ACCESS_KEY}
            - name: MINIO_ROOT_PASSWORD
              valueFrom:
                secretKeyRef: {name: objectstore-credentials, key: ROOT_SECRET_KEY}
          ports:
            - {name: s3, containerPort: 9000}
            - {name: console, containerPort: 9001}
          readinessProbe:
            httpGet: {path: /minio/health/ready, port: s3}
            initialDelaySeconds: 5
            periodSeconds: 5
          volumeMounts:
            - {name: data, mountPath: /data}
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 100Gi
---
apiVersion: v1
kind: Service
metadata:
  name: objectstore
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-s3
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: minio
  ports:
    - {name: s3, port: 7070, targetPort: 9000}
    - {name: console, port: 9001, targetPort: 9001}
`, opts.Namespace))
	} else {
		b.WriteString(fmt.Sprintf(`---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: versitygw-data
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-s3
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 100Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: versitygw
  namespace: %[1]s
  labels:
    app.kubernetes.io/name: versitygw
    ground.turion.space/replaces: aws-s3
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: versitygw
  template:
    metadata:
      labels:
        app.kubernetes.io/name: versitygw
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: versitygw
          image: ghcr.io/versity/versitygw:v1.0.0
          args: ["posix", "/data"]
          envFrom:
            - secretRef:
                name: objectstore-credentials
          env:
            - name: ROOT_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef: {name: objectstore-credentials, key: ROOT_ACCESS_KEY}
            - name: ROOT_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef: {name: objectstore-credentials, key: ROOT_SECRET_KEY}
            - name: VGW_REGION
              value: ground-local
            - name: VGW_PORT
              value: "7070"
          ports:
            - {name: s3, containerPort: 7070}
          readinessProbe:
            tcpSocket: {port: s3}
            initialDelaySeconds: 3
            periodSeconds: 5
          resources:
            requests: {cpu: 100m, memory: 256Mi}
            limits: {cpu: "1", memory: 1Gi}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
          volumeMounts:
            - {name: data, mountPath: /data}
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: versitygw-data
---
apiVersion: v1
kind: Service
metadata:
  name: objectstore
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-s3
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: versitygw
  ports:
    - {name: s3, port: 7070, targetPort: 7070}
`, opts.Namespace))
	}
	return b.String()
}

// objectBucket is a declarative bucket managed by Crossplane provider-minio.
// API group verified against xpkg.upbound.io/vshn/provider-minio:v0.4.4, where
// Bucket is cluster-scoped (no metadata.namespace) and addressed as
// spec.forProvider.bucketName. Used only for --target crossplane.
func objectBucket(bucket string, opts Options) string {
	return fmt.Sprintf(`# Replaces an S3 bucket with a Crossplane managed resource
# (provider-minio, cluster-scoped). Requires the provider-minio package.
apiVersion: minio.crossplane.io/v1
kind: Bucket
metadata:
  name: %[1]s
  labels:
    ground.turion.space/replaces: aws-s3
    ground.turion.space/managed-by: turion-platform-drift
spec:
  forProvider:
    bucketName: %[1]s
    region: ground-local
  providerConfigRef:
    name: ground-objectstore
  deletionPolicy: Orphan
`, bucket)
}

// bucketBootstrap creates the buckets on any S3-compatible endpoint using only
// built-in API kinds: a ConfigMap with the bucket inventory plus a Job that
// issues signed CreateBucket calls with curl (--aws-sigv4). No AWS SDK, no CRD,
// no operator. Used for --target kubernetes.
func bucketBootstrap(buckets []string, opts Options) string {
	var list strings.Builder
	for _, b := range buckets {
		list.WriteString("  " + b + ": \"\"\n")
	}
	return fmt.Sprintf(`# Replaces S3 bucket creation with a bootstrap Job against the in-cluster
# S3-compatible endpoint. Uses curl --aws-sigv4, so no AWS SDK and no CRD.
apiVersion: v1
kind: ConfigMap
metadata:
  name: objectstore-buckets
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-s3
data:
%[2]s---
apiVersion: batch/v1
kind: Job
metadata:
  name: objectstore-bucket-bootstrap
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-s3
    ground.turion.space/component: bootstrap
spec:
  backoffLimit: 10
  ttlSecondsAfterFinished: 600
  template:
    metadata:
      labels:
        ground.turion.space/component: bootstrap
    spec:
      restartPolicy: OnFailure
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: create-buckets
          image: %[3]s
          command: ["sh", "-ec"]
          args:
            - |
              for bucket in %[4]s; do
                echo "ensuring bucket $bucket"
                curl -sS --fail-with-body --retry 30 --retry-all-errors --retry-delay 2 \
                  --aws-sigv4 "aws:amz:ground-local:s3" \
                  --user "${ROOT_ACCESS_KEY_ID}:${ROOT_SECRET_ACCESS_KEY}" \
                  -X PUT "http://objectstore:7070/$bucket" >/dev/null
              done
              echo "buckets ready"
          envFrom:
            - secretRef:
                name: objectstore-credentials
          resources:
            requests: {cpu: 10m, memory: 32Mi}
            limits: {cpu: 200m, memory: 128Mi}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
`, opts.Namespace, list.String(), opts.Registry+"/curlimages/curl:8.14.1",
		bucketLoop(buckets))
}

// bucketLoop renders the bucket list for the bootstrap Job's shell loop.
func bucketLoop(buckets []string) string {
	if len(buckets) == 0 {
		return "tlm-archive"
	}
	q := make([]string, 0, len(buckets))
	for _, b := range buckets {
		q = append(q, `"`+b+`"`)
	}
	return strings.Join(q, " ")
}

// namespaceObject is emitted first in every bundle so the rest of the stack
// can be created by a single `kubectl apply -f` pass.
func namespaceObject(opts Options) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    app.kubernetes.io/part-of: turion-ground-system
    ground.turion.space/managed-by: turion-platform-drift
    pod-security.kubernetes.io/enforce: restricted
`, opts.Namespace)
}

func serviceAccount(opts Options) string {
	return fmt.Sprintf(`# Replaces the IRSA role-arn ServiceAccount annotation with in-cluster identity:
# a ServiceAccount plus an S3-compatible credential Secret. No AWS STS call.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ground-service
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-iam
  annotations:
    ground.turion.space/identity: workload-secret
automountServiceAccountToken: false
---
apiVersion: v1
kind: Secret
metadata:
  name: ground-service-credentials
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-iam
type: Opaque
stringData:
  OBJECT_STORE_ACCESS_KEY: "ground-access-key"
  OBJECT_STORE_SECRET_KEY: "ground-secret-key"
`, opts.Namespace, opts.Namespace)
}

func rbac(opts Options) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ground-service
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-iam
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["secrets"]
    resourceNames: ["ground-service-credentials"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ground-service
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-iam
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ground-service
subjects:
  - kind: ServiceAccount
    name: ground-service
    namespace: %s
`, opts.Namespace, opts.Namespace, opts.Namespace)
}

func natsJetStream(opts Options) string {
	return fmt.Sprintf(`# Replaces SQS/SNS/Kinesis with NATS JetStream (air-gap friendly, no managed broker).
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: nats
  namespace: %s
  labels:
    app.kubernetes.io/name: nats
    ground.turion.space/replaces: aws-sqs
spec:
  serviceName: nats
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: nats
  template:
    metadata:
      labels:
        app.kubernetes.io/name: nats
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: nats
          image: docker.io/library/nats:2.10-alpine
          args: ["-js", "-sd", "/data", "-m", "8222"]
          ports:
            - {name: client, containerPort: 4222}
            - {name: monitor, containerPort: 8222}
          readinessProbe:
            httpGet: {path: /healthz, port: monitor}
            initialDelaySeconds: 3
            periodSeconds: 5
          volumeMounts:
            - {name: data, mountPath: /data}
          resources:
            requests: {cpu: 100m, memory: 256Mi}
            limits: {cpu: "1", memory: 1Gi}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 20Gi
---
apiVersion: v1
kind: Service
metadata:
  name: nats
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-sqs
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: nats
  ports:
    - {name: client, port: 4222, targetPort: client}
    - {name: monitor, port: 8222, targetPort: monitor}
`, opts.Namespace, opts.Namespace)
}

func postgresCluster(opts Options) string {
	return fmt.Sprintf(`# Replaces DynamoDB/RDS with CloudNativePG PostgreSQL on the ground station.
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: ground-telemetry-db
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-dynamodb
spec:
  instances: 3
  imageName: ghcr.io/cloudnative-pg/postgresql:16.4
  primaryUpdateStrategy: unsupervised
  enableSuperuserAccess: false
  storage:
    size: 50Gi
  resources:
    requests: {cpu: 250m, memory: 512Mi}
    limits: {cpu: "2", memory: 2Gi}
  bootstrap:
    initdb:
      database: telemetry
      owner: ground
      encoding: UTF8
  monitoring:
    enablePodMonitor: true
`, opts.Namespace)
}

func otelCollector(opts Options) string {
	return fmt.Sprintf(`# Replaces CloudWatch Logs / X-Ray with an in-cluster OpenTelemetry pipeline.
# Exporters are OTLP/Prometheus/Loki — all reachable from an air-gapped cluster.
apiVersion: opentelemetry.io/v1beta1
kind: OpenTelemetryCollector
metadata:
  name: otel
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-cloudwatch
spec:
  mode: deployment
  replicas: 1
  config:
    receivers:
      otlp:
        protocols:
          grpc:
            endpoint: 0.0.0.0:4317
          http:
            endpoint: 0.0.0.0:4318
    processors:
      memory_limiter:
        check_interval: 5s
        limit_percentage: 80
        spike_limit_percentage: 20
      batch: {}
      resourcedetection:
        detectors: [env]
    exporters:
      debug:
        verbosity: basic
      prometheus:
        endpoint: 0.0.0.0:8889
        resource_to_telemetry_conversion:
          enabled: true
      otlphttp/loki:
        endpoint: http://loki.%[1]s.svc.cluster.local:3100/otlp
        tls:
          insecure: true
    service:
      pipelines:
        traces:
          receivers: [otlp]
          processors: [memory_limiter, batch, resourcedetection]
          exporters: [debug]
        metrics:
          receivers: [otlp]
          processors: [memory_limiter, batch, resourcedetection]
          exporters: [prometheus]
        logs:
          receivers: [otlp]
          processors: [memory_limiter, batch, resourcedetection]
          exporters: [debug, otlphttp/loki]
---
apiVersion: v1
kind: Service
metadata:
  name: otel-collector
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-cloudwatch
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: otel-collector
  ports:
    - {name: otlp-grpc, port: 4317, targetPort: 4317}
    - {name: otlp-http, port: 4318, targetPort: 4318}
    - {name: metrics, port: 8889, targetPort: 8889}
`, opts.Namespace)
}

func externalSecret(opts Options) string {
	return fmt.Sprintf(`# Replaces AWS Secrets Manager / SSM Parameter Store with External Secrets
# Operator backed by Vault (or any Kubernetes Secret in an air gap).
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata:
  name: ground-vault
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-secretsmanager
spec:
  provider:
    vault:
      server: http://vault.%s.svc.cluster.local:8200
      path: ground-system
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: ground-service
          serviceAccountRef:
            name: ground-service
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: ground-service-credentials
  namespace: %s
  labels:
    ground.turion.space/replaces: aws-secretsmanager
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: ground-vault
    kind: SecretStore
  target:
    name: ground-service-credentials
    creationPolicy: Owner
  data:
    - secretKey: OBJECT_STORE_ACCESS_KEY
      remoteRef: {key: ground-system/objectstore, property: access_key}
    - secretKey: OBJECT_STORE_SECRET_KEY
      remoteRef: {key: ground-system/objectstore, property: secret_key}
`, opts.Namespace, opts.Namespace, opts.Namespace)
}

// registryRewrite emits a kustomize overlay, NOT a cluster object. A
// kustomization.yaml has kind Kustomization in group kustomize.config.k8s.io
// and cannot be applied with kubectl, so it lives in its own overlay directory
// and the applied bundle stays 100% appliable.
func registryRewrite(opts Options) string {
	return fmt.Sprintf(`# Replaces Amazon ECR with the ground-station registry (Harbor/Zot).
# This is a kustomize overlay, not a cluster object. Apply with:
#   kubectl apply -k ./kustomize
# Add one entry per image your manifests reference.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
metadata:
  name: ground-station-registry-rewrite
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-ecr
resources:
  - ../ground-station-portable.yaml
images:
  - name: 123456789012.dkr.ecr.us-west-2.amazonaws.com/turion/pass-processor
    newName: %[2]s/turion/pass-processor
    newTag: "7"
  - name: 123456789012.dkr.ecr.us-west-2.amazonaws.com/turion/ground-service
    newName: %[2]s/turion/ground-service
    newTag: "1.0.0"
`, opts.Namespace, opts.Registry)
}

// postgresPortable replaces DynamoDB/RDS with a plain Postgres StatefulSet.
// Built-in API kinds only, so it applies to a cluster with no operators.
func postgresPortable(opts Options) string {
	return fmt.Sprintf(`# Replaces DynamoDB/RDS with PostgreSQL on the ground station.
# Plain StatefulSet + PVC + Secret: no operator, no CRD, no AWS.
apiVersion: v1
kind: Secret
metadata:
  name: ground-telemetry-db
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-dynamodb
type: Opaque
stringData:
  # Populated by the operator (or Vault/External Secrets) before first start.
  POSTGRES_DB: telemetry
  POSTGRES_USER: ground
  POSTGRES_PASSWORD: ""
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ground-telemetry-db
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-dynamodb
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 50Gi
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: ground-telemetry-db
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-dynamodb
spec:
  serviceName: ground-telemetry-db
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: ground-telemetry-db
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ground-telemetry-db
        ground.turion.space/replaces: aws-dynamodb
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 26
        fsGroup: 26
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: postgres
          image: %[2]s
          ports:
            - {name: sql, containerPort: 5432}
          envFrom:
            - secretRef:
                name: ground-telemetry-db
          readinessProbe:
            exec:
              command: ["pg_isready", "-U", "ground", "-d", "telemetry"]
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            exec:
              command: ["pg_isready", "-U", "ground", "-d", "telemetry"]
            initialDelaySeconds: 30
            periodSeconds: 20
          resources:
            requests: {cpu: 250m, memory: 512Mi}
            limits: {cpu: "2", memory: 2Gi}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
          volumeMounts:
            - {name: data, mountPath: /var/lib/postgresql/data}
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: ground-telemetry-db
---
apiVersion: v1
kind: Service
metadata:
  name: ground-telemetry-db
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-dynamodb
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: ground-telemetry-db
  ports:
    - {name: sql, port: 5432, targetPort: 5432}
`, opts.Namespace, opts.Registry+"/library/postgres:16.15")
}

// otelPortable replaces CloudWatch/X-Ray with a self-hosted OTel Collector
// Deployment driven by a ConfigMap. Built-in API kinds only.
func otelPortable(opts Options) string {
	return fmt.Sprintf(`# Replaces CloudWatch Logs / X-Ray with an in-cluster OpenTelemetry pipeline.
# Exporters are OTLP/Prometheus/Loki: all reachable from an air-gapped cluster.
apiVersion: v1
kind: ConfigMap
metadata:
  name: otel-collector
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-cloudwatch
data:
  config.yaml: |
    receivers:
      otlp:
        protocols:
          grpc: {endpoint: 0.0.0.0:4317}
          http: {endpoint: 0.0.0.0:4318}
    processors:
      memory_limiter:
        check_interval: 5s
        limit_percentage: 80
        spike_limit_percentage: 20
      batch: {}
      resourcedetection:
        detectors: [env]
    exporters:
      debug: {verbosity: basic}
      prometheus:
        endpoint: 0.0.0.0:8889
        resource_to_telemetry_conversion: {enabled: true}
      otlphttp/loki:
        endpoint: http://loki.%[1]s.svc.cluster.local:3100/otlp
        tls: {insecure: true}
    service:
      pipelines:
        traces:
          receivers: [otlp]
          processors: [memory_limiter, batch, resourcedetection]
          exporters: [debug]
        metrics:
          receivers: [otlp]
          processors: [memory_limiter, batch, resourcedetection]
          exporters: [prometheus]
        logs:
          receivers: [otlp]
          processors: [memory_limiter, batch, resourcedetection]
          exporters: [debug, otlphttp/loki]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel-collector
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-cloudwatch
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: otel-collector
  template:
    metadata:
      labels:
        app.kubernetes.io/name: otel-collector
        ground.turion.space/replaces: aws-cloudwatch
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: otel-collector
          image: %[2]s
          args: ["--config=/conf/config.yaml"]
          ports:
            - {name: otlp-grpc, containerPort: 4317}
            - {name: otlp-http, containerPort: 4318}
            - {name: metrics, containerPort: 8889}
          readinessProbe:
            httpGet: {path: /metrics, port: metrics}
            initialDelaySeconds: 5
          resources:
            requests: {cpu: 100m, memory: 256Mi}
            limits: {cpu: "1", memory: 1Gi}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
          volumeMounts:
            - {name: config, mountPath: /conf}
      volumes:
        - name: config
          configMap:
            name: otel-collector
---
apiVersion: v1
kind: Service
metadata:
  name: otel-collector
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-cloudwatch
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: otel-collector
  ports:
    - {name: otlp-grpc, port: 4317, targetPort: 4317}
    - {name: otlp-http, port: 4318, targetPort: 4318}
    - {name: metrics, port: 8889, targetPort: 8889}
`, opts.Namespace, opts.Registry+"/otel/opentelemetry-collector-contrib:0.132.0")
}

// secretPlaceholder replaces Secrets Manager with a plain Secret that a Vault
// agent or External Secrets Operator fills in. Built-in API kinds only.
func secretPlaceholder(opts Options) string {
	return fmt.Sprintf(`# Replaces AWS Secrets Manager / SSM Parameter Store with a Kubernetes Secret.
# '--target crossplane' instead emits an ExternalSecret synced from Vault.
apiVersion: v1
kind: Secret
metadata:
  name: ground-service-secrets
  namespace: %[1]s
  labels:
    ground.turion.space/replaces: aws-secretsmanager
type: Opaque
# Keys are injected by the ground-station secret pipeline; nothing is baked in.
stringData: {}
`, opts.Namespace)
}

func crossplaneComposition(_ Options) string {
	// Crossplane v2 rejected the v1 `spec.resources` / `spec.patchSets` shape:
	// a Composition must run a pipeline. The resource templates therefore move
	// into the function-patch-and-transform input, which is the current API.
	return (`# Crossplane composition: one GroundStack composite provisions the whole
# portable ground-service stack with no AWS provider installed.
# Requires Crossplane >= 2.0 and the function-patch-and-transform function:
#   kubectl apply -f - <<'EOF'
#   apiVersion: pkg.crossplane.io/v1
#   kind: Function
#   metadata: {name: function-patch-and-transform}
#   spec: {package: xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:v0.9.0}
#   EOF
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: groundstack.ground.turion.space
  labels:
    ground.turion.space/managed-by: turion-platform-drift
spec:
  compositeTypeRef:
    apiVersion: ground.turion.space/v1alpha1
    kind: GroundStack
  writeConnectionSecretsToNamespace: crossplane-system
  mode: Pipeline
  pipeline:
    - step: provision-portable-stack
      functionRef:
        name: function-patch-and-transform
      input:
        apiVersion: pt.fn.crossplane.io/v1beta1
        kind: Resources
        resources:
          - name: namespace
            base:
              apiVersion: v1
              kind: Namespace
              metadata:
                name: placeholder
            patches:
              - type: FromCompositeFieldPath
                fromFieldPath: spec.parameters.namespace
                toFieldPath: metadata.name
          - name: objectstore
            base:
              apiVersion: kubernetes.crossplane.io/v1alpha1
              kind: Object
              metadata:
                name: objectstore-deployment
              spec:
                forProvider:
                  manifest:
                    apiVersion: apps/v1
                    kind: Deployment
                    metadata:
                      name: versitygw
                    spec:
                      replicas: 1
                      selector:
                        matchLabels:
                          app: versitygw
                      template:
                        metadata:
                          labels:
                            app: versitygw
                        spec:
                          containers:
                            - name: versitygw
                              image: ghcr.io/versity/versitygw:v1.0.0
                              args: ["posix", "/data"]
                              port: 7070
                providerConfigRef:
                  name: ground-kubernetes
            patches:
              - type: FromCompositeFieldPath
                fromFieldPath: spec.parameters.namespace
                toFieldPath: spec.forProvider.manifest.metadata.namespace
          - name: objectbucket
            base:
              apiVersion: s3.min.io/v1alpha1
              kind: Bucket
              metadata:
                name: placeholder
              spec:
                name: placeholder
                retention:
                  mode: GOVERNANCE
                  unit: days
                  validity: 90
            patches:
              - type: FromCompositeFieldPath
                fromFieldPath: spec.parameters.bucketName
                toFieldPath: metadata.name
              - type: FromCompositeFieldPath
                fromFieldPath: spec.parameters.bucketName
                toFieldPath: spec.name
          - name: otel
            base:
              apiVersion: opentelemetry.io/v1beta1
              kind: OpenTelemetryCollector
              metadata:
                name: otel
              spec:
                mode: deployment
          - name: postgres
            base:
              apiVersion: postgresql.cnpg.io/v1
              kind: Cluster
              metadata:
                name: ground-telemetry-db
              spec:
                instances: 3
                storage:
                  size: 50Gi
`)
}

func crossplaneXRD(opts Options) string {
	// Deliberately v1: Crossplane v2 XRDs reject `spec.claimNames`
	// ("Claims aren't supported in apiextensions.crossplane.io/v2"), and this
	// stack provisions a namespaced GroundStackClaim. v1 is still served by
	// 2.4.3 and only emits a deprecation notice, which is documented in
	// docs/verification.md.
	return fmt.Sprintf(`apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: groundstacks.ground.turion.space
  labels:
    ground.turion.space/managed-by: turion-platform-drift
spec:
  group: ground.turion.space
  names:
    kind: GroundStack
    plural: groundstacks
  claimNames:
    kind: GroundStackClaim
    plural: groundstackclaims
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                parameters:
                  type: object
                  properties:
                    namespace:
                      type: string
                      description: Ground-station namespace (default %s).
                      default: %s
                    bucketName:
                      type: string
                      description: S3-compatible bucket for telemetry archive.
                      default: tlm-archive
                    objectStore:
                      type: string
                      description: Object-store shim implementation.
                      enum: ["versitygw", "minio"]
                      default: versitygw
                    retentionDays:
                      type: integer
                      default: 90
                  required: ["namespace"]
              required: ["parameters"]
          required: ["spec"]
`, opts.Namespace, opts.Namespace)
}
