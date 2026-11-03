#!/usr/bin/env bash
# End-to-end verification: everything the Definition of Done claims, executed.
#
#   scripts/verify.sh            full run (needs network once, for the cluster)
#   scripts/verify.sh --no-cluster   skip cluster-dependent gates
#
# Exit code 0 only if every gate passes. Every gate prints PASS/FAIL and the
# summary at the end is what CI consumes.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export PATH="$HOME/.local/bin:/usr/local/go/bin:/usr/local/bin:$PATH"

CLUSTER=1
[[ "${1:-}" == "--no-cluster" ]] && CLUSTER=0

PASS=0
FAIL=0
declare -a RESULTS=()

gate() { # gate <name> <command...>
  local name="$1"; shift
  local start end ms
  start=$(date +%s%N)
  if "$@" >/tmp/verify-gate.log 2>&1; then
    end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
    PASS=$((PASS+1)); RESULTS+=("PASS  ${name}  (${ms}ms)")
    printf '  \033[32mPASS\033[0m  %-52s %6sms\n' "$name" "$ms"
  else
    end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
    FAIL=$((FAIL+1)); RESULTS+=("FAIL  ${name}  (${ms}ms)")
    printf '  \033[31mFAIL\033[0m  %-52s %6sms\n' "$name" "$ms"
    sed 's/^/        /' /tmp/verify-gate.log | tail -25
  fi
}

section() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

GO=go
if ! command -v go >/dev/null 2>&1; then GO=/usr/local/go/bin/go; fi

section "1. hygiene"
gate "gofmt reports no diffs" bash -c '[ -z "$(gofmt -l .)" ]'
gate "go vet" "$GO" vet ./...
gate "go build" "$GO" build -trimpath -o /tmp/tpd-verify .
gate "zero external Go dependencies" bash -c \
  '! grep -q "^require" go.mod'

section "2. correctness (tests, race, coverage of every rule)"
gate "go test ./..." "$GO" test -count=1 ./...
gate "go test -race ./..." "$GO" test -race -count=1 ./...
gate "every rule fires on its own probe" "$GO" test -count=1 -run TestOneProbePerRuleFires .
gate "portable corpus produces zero findings" "$GO" test -count=1 -run TestPortableCorpusIsSilent .

section "3. stress and adversarial input"
gate "3000-file repository inside the 30s budget" "$GO" test -count=1 -run TestStressLargeRepository .
gate "adversarial tree (CRLF/BOM/NUL/binary/deep YAML)" "$GO" test -count=1 -run TestAdversarialFiles .
gate "scan is deterministic" "$GO" test -count=1 -run TestScanIsDeterministic .
gate "generation is idempotent" "$GO" test -count=1 -run TestGenerateIsIdempotent .

section "4. fuzzing (short budget; CI runs longer)"
gate "fuzz yamlite parser" "$GO" test -run XXX -fuzz FuzzParse -fuzztime 20s ./internal/yamlite/
gate "fuzz manifest scanner" "$GO" test -run XXX -fuzz FuzzScanText -fuzztime 15s .
gate "fuzz Go AST analyzer" "$GO" test -run XXX -fuzz FuzzScanGoSource -fuzztime 10s .

section "5. Definition-of-Done scan: scorecard under 30s"
start=$(date +%s%N)
"$GO" run turion-platform-drift scan ./ground-service >/tmp/verify-scorecard.txt 2>&1
rc=$?; end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
if [[ $rc -eq 0 && $ms -lt 30000 ]]; then
  PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m  %-52s %6sms\n' "go run turion-platform-drift scan ./ground-service" "$ms"
else
  FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m  %-52s %6sms\n' "scan" "$ms"; cat /tmp/verify-scorecard.txt | tail -20
fi
gate "all 26 rules fire on the AWS-bound fixture" bash -c \
  'python3 -c "
import json,sys
d=json.load(open(\"reports/scorecard.json\")) if __import__(\"os\").path.exists(\"reports/scorecard.json\") else None
" ; '"$GO"' run turion-platform-drift scan ./ground-service --format json | python3 -c "
import json,sys
d=json.load(sys.stdin)
n=d[\"counts\"][\"distinct_rules\"]
sys.exit(0 if n>=26 else 1)
"'

section "6. generation + air-gapped validation (no cluster needed)"
rm -rf reports reports-cp
gate "generate kubernetes target" bash -c \
  '"'"$GO"'" run turion-platform-drift scan ./ground-service --generate --out ./reports --target kubernetes --fail-on-residual-aws'
gate "generate crossplane target" bash -c \
  '"'"$GO"'" run turion-platform-drift scan ./ground-service --generate --out ./reports-cp --target crossplane --fail-on-residual-aws'
gate "validate kubernetes bundle (embedded schema)" "$GO" run turion-platform-drift validate ./reports
gate "validate crossplane bundle (embedded schema)" "$GO" run turion-platform-drift validate ./reports-cp
gate "validate reference templates + render Helm chart" "$GO" run turion-platform-drift validate ./templates
gate "kubernetes bundle contains only builtin kinds" bash -c \
  '! grep -E "^kind: (Bucket|Cluster|OpenTelemetryCollector|ExternalSecret|SecretStore|Composition|CompositeResourceDefinition)$" reports/ground-station-portable.yaml'
gate "no residual AWS in either bundle" bash -c \
  '! grep -rE "arn:aws|\.amazonaws\.com|s3://|eks\.amazonaws\.com" reports/ground-station-portable.yaml reports-cp/ground-station-portable.yaml'
gate "no public image registries in either bundle" bash -c \
  '! grep -rE "(image|imageName):[[:space:]]*(ghcr\.io|docker\.io|quay\.io|public\.ecr\.aws)/" reports/ground-station-portable.yaml reports-cp/ground-station-portable.yaml'

if [[ $CLUSTER -eq 1 ]]; then
  section "7. real cluster gates (kwok: etcd + kube-apiserver, no Docker)"
  if bash scripts/kwok-cluster.sh up "${KWOK_CLUSTER:-ground}" >/tmp/verify-cluster.log 2>&1; then
    export KUBECONFIG="$HOME/.kwok/clusters/${KWOK_CLUSTER:-ground}/kubeconfig.yaml"
    gate "install upstream CRDs at current versions" bash scripts/install-crds.sh
    gate "kubectl apply --dry-run=client: kubernetes bundle" \
      kubectl apply --dry-run=client -f reports/ground-station-portable.yaml
    # Server-side dry-run never persists anything, so the Namespace document at
    # the top of the bundle does not exist by the time the first namespaced
    # object is admitted ('namespaces "ground-system" not found'). Every real
    # deployment creates the namespace first, so the gate does the same; the
    # remaining 26 objects are still validated by a clean cluster's admission.
    kubectl create namespace "${TPD_NAMESPACE:-ground-system}" \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    gate "kubectl apply --dry-run=server: kubernetes bundle" \
      kubectl apply --dry-run=server -f reports/ground-station-portable.yaml
    gate "kubectl apply --dry-run=client: crossplane bundle" \
      kubectl apply --dry-run=client -f reports-cp/ground-station-portable.yaml
    # Server-side dry-run is the only gate that both validates every object's
    # schema AND runs admission, without depending on what a previous run left
    # behind. It is what caught an XRD emitted at apiextensions.crossplane.io/v2
    # ("Claims aren't supported in ... v2"), which a re-apply over existing
    # objects happily reported as "configured".
    gate "kubectl apply --dry-run=server: crossplane bundle" \
      kubectl apply --dry-run=server -f reports-cp/ground-station-portable.yaml
    gate "REAL apply: kubernetes bundle (bare cluster, no operators)" bash -c '
      ns="verify-$(date +%s)"
      sed "s/namespace: ground-system/namespace: $ns/; s/name: ground-system/name: $ns/" \
        reports/ground-station-portable.yaml > /tmp/verify-bundle.yaml
      out=$(kubectl apply -f /tmp/verify-bundle.yaml 2>&1)
      echo "$out"
      ! grep -qi "warning\|error" <<<"$out"
    '
    gate "REAL apply: crossplane bundle (CRDs installed)" bash -c '
      out=$(kubectl apply -f reports-cp/ground-station-portable.yaml 2>&1)
      echo "$out"
      # Nothing may be rejected ("invalid"/"error") and nothing may be silently
      # skipped: every one of the 27 objects must be created, configured or
      # reported unchanged.
      ! grep -qiE "invalid|error" <<<"$out"
      [ "$(grep -cE " (created|configured|unchanged)$" <<<"$out")" -eq 27 ]
    '
    gate "validator kubectl gate is enforced (--require-kubectl)" \
      "$GO" run turion-platform-drift validate ./reports --require-kubectl --server-dry-run
  else
    FAIL=$((FAIL+1)); RESULTS+=("FAIL  cluster setup")
    printf '  \033[31mFAIL\033[0m  cluster setup\n'
    tail -20 /tmp/verify-cluster.log | sed 's/^/        /'
  fi
else
  printf '\n  (cluster gates skipped: --no-cluster)\n'
fi

printf '\n\033[1m== summary\033[0m\n'
printf '  %d passed, %d failed\n\n' "$PASS" "$FAIL"
for r in "${RESULTS[@]}"; do echo "  $r"; done
[[ $FAIL -eq 0 ]] || exit 1
