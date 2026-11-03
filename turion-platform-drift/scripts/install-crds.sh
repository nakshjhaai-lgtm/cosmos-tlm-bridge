#!/usr/bin/env bash
# Install the CRDs the --target crossplane bundle depends on, at the versions
# that actually exist upstream. Every API group below is pinned to a release we
# verified against the live API server (see docs/verification.md).
#
#   crossplane v2.4.3          apiextensions.crossplane.io/v1, pkg.crossplane.io/v1
#   provider-minio v0.4.4      minio.crossplane.io/v1 (Bucket, ProviderConfig)
#   cloudnative-pg (helm)      postgresql.cnpg.io/v1 (Cluster)
#   external-secrets (helm)    external-secrets.io/v1 (ExternalSecret, SecretStore)
#   opentelemetry-operator     opentelemetry.io/v1beta1 (OpenTelemetryCollector)
set -euo pipefail

CROSSPLANE_VERSION="${CROSSPLANE_VERSION:-v2.4.3}"
PROVIDER_MINIO_VERSION="${PROVIDER_MINIO_VERSION:-v0.4.4}"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
mkdir -p "$WORKDIR"
cd "$WORKDIR"

BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
HELM_VERSION="${HELM_VERSION:-v3.16.3}"
mkdir -p "$BIN_DIR"
export PATH="$BIN_DIR:$PATH"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing $1" >&2; exit 1; }; }

# helm is fetched when absent so this script works on a bare machine, the same
# way scripts/kwok-cluster.sh fetches kwokctl and kubectl.
if ! command -v helm >/dev/null 2>&1; then
  echo "==> installing helm $HELM_VERSION into $BIN_DIR"
  curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"
  tar -xzf /tmp/helm.tgz -C /tmp linux-amd64/helm
  mv /tmp/linux-amd64/helm "$BIN_DIR/helm"
  chmod +x "$BIN_DIR/helm"
fi

need kubectl
need helm
need python3

echo "==> helm repos"
helm repo add cnpg https://cloudnative-pg.github.io/charts >/dev/null 2>&1 || true
helm repo add external-secrets https://charts.external-secrets.io >/dev/null 2>&1 || true
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null 2>&1 || true
helm repo update >/dev/null

echo "==> crossplane $CROSSPLANE_VERSION CRDs"
for f in apiextensions.crossplane.io_compositeresourcedefinitions.yaml \
         apiextensions.crossplane.io_compositions.yaml \
         pkg.crossplane.io_providers.yaml \
         pkg.crossplane.io_providerconfigs.yaml; do
  curl -fsSL -o "cp-$f" \
    "https://raw.githubusercontent.com/crossplane/crossplane/${CROSSPLANE_VERSION}/cluster/crds/$f" 2>/dev/null \
    || echo "   (skip $f: not present in $CROSSPLANE_VERSION)"
done

echo "==> provider-minio $PROVIDER_MINIO_VERSION CRDs"
for f in minio.crossplane.io_buckets.yaml minio.crossplane.io_providerconfigs.yaml; do
  curl -fsSL -o "pm-$f" \
    "https://raw.githubusercontent.com/vshn/provider-minio/${PROVIDER_MINIO_VERSION}/package/crds/$f"
done

echo "==> chart CRDs (cnpg / external-secrets / opentelemetry-operator)"
helm template cnpg cnpg/cloudnative-pg --include-crds > cnpg.yaml
helm template eso external-secrets/external-secrets --include-crds > eso.yaml
helm template otel open-telemetry/opentelemetry-operator --include-crds > otel.yaml

echo "==> extracting CustomResourceDefinitions only"
python3 - <<'PY'
import pathlib
out = []
for f in sorted(pathlib.Path('.').glob('*.yaml')):
    for doc in f.read_text().split('\n---'):
        if 'kind: CustomResourceDefinition' in doc:
            out.append('---\n' + doc.strip() + '\n')
pathlib.Path('crds-only.yaml').write_text(''.join(out))
print(f"   {sum(1 for _ in open('crds-only.yaml'))} lines")
PY

echo "==> applying"
kubectl apply --server-side --force-conflicts -f crds-only.yaml >/dev/null
kubectl wait --for condition=established --timeout=120s -f crds-only.yaml >/dev/null 2>&1 || true

echo "==> served versions for the kinds the generator emits"
for crd in buckets.minio.crossplane.io providerconfigs.minio.crossplane.io \
           compositions.apiextensions.crossplane.io \
           compositeresourcedefinitions.apiextensions.crossplane.io \
           providers.pkg.crossplane.io clusters.postgresql.cnpg.io \
           opentelemetrycollectors.opentelemetry.io \
           externalsecrets.external-secrets.io secretstores.external-secrets.io; do
  printf '   %-52s ' "$crd"
  kubectl get crd "$crd" -o jsonpath='{range .spec.versions[*]}{.name}{"(served="}{.served}{") "}{end}' 2>/dev/null || printf 'MISSING'
  echo
done
