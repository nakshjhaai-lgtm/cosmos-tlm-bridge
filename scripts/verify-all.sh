#!/usr/bin/env bash
# One-shot Definition-of-Done harness for both bottlenecks.
#
#   bash scripts/verify-all.sh                 # all gates available on this host
#   RUN_CLUSTER=1 bash scripts/verify-all.sh  # also run the real kube-apiserver gates
#
# Output is appended to docs/evidence/verification-all.log (or $VERIFY_LOG).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
LOG="${VERIFY_LOG:-$ROOT/docs/evidence/verification-all.log}"
mkdir -p "$(dirname "$LOG")"
: > "$LOG"
PASS=0; FAIL=0; SKIP=0
declare -a RESULTS=()

have() { command -v "$1" >/dev/null 2>&1; }

gate() {
  local name="$1"; shift
  local start end ms rc
  start=$(date +%s%N)
  if "$@" >>"$LOG" 2>&1; then rc=0; else rc=$?; fi
  end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
  if [[ $rc -eq 0 ]]; then
    PASS=$((PASS+1)); RESULTS+=("PASS  $name  ${ms}ms")
    printf '\033[32mPASS\033[0m  %-56s %6sms\n' "$name" "$ms"
  else
    FAIL=$((FAIL+1)); RESULTS+=("FAIL  $name  ${ms}ms (rc=$rc)")
    printf '\033[31mFAIL\033[0m  %-56s %6sms  see %s\n' "$name" "$ms" "$LOG"
    tail -25 "$LOG" | sed 's/^/        /'
  fi
  return 0
}

skip() {
  local name="$1" reason="$2"
  SKIP=$((SKIP+1)); RESULTS+=("SKIP  $name  $reason")
  printf '\033[33mSKIP\033[0m  %-56s %s\n' "$name" "$reason"
}

section() { printf '\n== %s ==\n' "$1" | tee -a "$LOG"; }

echo "cosmos-tlm-bridge / turion-platform-drift — verification $(date -u +%FT%TZ)" | tee -a "$LOG" >/dev/null
echo "root=$ROOT" | tee -a "$LOG" >/dev/null

# Use a project-local venv when the host Python is externally managed (PEP 668).
PY="${PY:-python3}"
if ! "$PY" -c 'import pydantic, yaml, pytest' >/dev/null 2>&1; then
  python3 -m venv .venv
  .venv/bin/pip install -q --disable-pip-version-check -e '.[dev]'
  PY=.venv/bin/python
fi
export PY
export PYTHONPATH="$ROOT/src${PYTHONPATH:+:$PYTHONPATH}"
PYBIN="$(cd "$(dirname "$PY")" && pwd)"
export PATH="$PYBIN:$PATH"

section "1. Python package and acceptance"
gate "Python >= 3.11" "$PY" -c 'import sys; assert sys.version_info >= (3,11)'
gate "Python dependencies + package import" "$PY" -c 'import pydantic, yaml, pytest, cosmos_tlm_bridge'
gate "pytest (80 tests)" "$PY" -m pytest -q
PORT="$($PY -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
export PORT COSMOS_PORT="$PORT" COSMOS_URL="http://127.0.0.1:$PORT"

gate "cosmos-tlm-bridge demo < 45 s" bash -c '
  start=$(date +%s%N); $PY -m cosmos_tlm_bridge.cli demo --port "$PORT" --out /tmp/ctb-demo >/tmp/ctb-demo.log 2>&1
  rc=$?; end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
  cat /tmp/ctb-demo.log; echo "demo ${ms}ms"
  [[ $rc -eq 0 && $ms -lt 45000 ]]
'
# A live reference server for the declarative CI runner.
$PY -m cosmos_tlm_bridge.cli serve --host 127.0.0.1 --port "$PORT" >/tmp/ctb-serve.log 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1 && break; sleep 0.2; done
gate "full CI acceptance sweep < 105 s" bash -c '
  start=$(date +%s%N)
  for s in droid_smoke droid_thermal_red droid_pass_acceptance; do
    $PY -m cosmos_tlm_bridge.cli run "suites/$s.yaml" --out /tmp/ctb-suites || exit 1
  done
  end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
  echo "suite sweep ${ms}ms"; [[ $ms -lt 105000 ]]
'
gate "typed bridge + webhook + YELLOW_HIGH pipeline" "$PY" scripts/verify_pipeline.py --duration 12
gate "no Ruby source anywhere" bash -c '! find . -name "*.rb" -type f | grep -q .'
kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true
trap - EXIT
unset COSMOS_PORT COSMOS_URL

section "2. Go typed bridge and portability linter"
if have go; then
  gate "go vet: typed bridge" bash -c 'cd go && go vet ./...'
  gate "go test: typed bridge" bash -c 'cd go && go test ./...'
  gate "gofmt clean: linter" bash -c 'cd turion-platform-drift && test -z "$(gofmt -l .)"'
  gate "go vet: linter" bash -c 'cd turion-platform-drift && go vet ./...'
  gate "go test: linter" bash -c 'cd turion-platform-drift && go test ./...'
  gate "go test -race: linter" bash -c 'cd turion-platform-drift && go test -race ./...'
  gate "DoD scan < 30 s" bash -c '
    cd turion-platform-drift
    start=$(date +%s%N)
    go run turion-platform-drift scan ./ground-service >/tmp/tpd-scorecard.txt 2>&1
    rc=$?; end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
    cat /tmp/tpd-scorecard.txt; echo "scan ${ms}ms"
    [[ $rc -eq 0 && $ms -lt 30000 ]]
  '
  gate "generate + validate both targets" bash -c '
    cd turion-platform-drift
    rm -rf /tmp/tpd-reports /tmp/tpd-reports-cp
    go run turion-platform-drift scan ./ground-service --generate --out /tmp/tpd-reports --target kubernetes --fail-on-residual-aws &&
    go run turion-platform-drift scan ./ground-service --generate --out /tmp/tpd-reports-cp --target crossplane --fail-on-residual-aws &&
    go run turion-platform-drift validate /tmp/tpd-reports &&
    go run turion-platform-drift validate /tmp/tpd-reports-cp
  '
  gate "zero AWS / public-registry references" bash -c '
    ! grep -rE "arn:aws|\.amazonaws\.com|s3://|eks\.amazonaws\.com|(image|imageName):[[:space:]]*(ghcr\.io|docker\.io|quay\.io|public\.ecr\.aws)/" \
      /tmp/tpd-reports/ground-station-portable.yaml /tmp/tpd-reports-cp/ground-station-portable.yaml
  '
  gate "all 26 linter rules fire" bash -c '
    cd turion-platform-drift
    go run turion-platform-drift scan ./ground-service --format json |
      python3 -c "import json,sys; d=json.load(sys.stdin); assert d[\"counts\"][\"distinct_rules\"] >= 26"
  '
  gate "linter accuracy matrix" bash -c 'cd turion-platform-drift && go test -run "TestOneProbePerRuleFires|TestPortableCorpusIsSilent|TestGeneratedImagesUseGroundRegistry" ./...'
  gate "linter harness (no cluster)" bash -c 'cd turion-platform-drift && bash scripts/verify.sh --no-cluster'
  if [[ "${RUN_CLUSTER:-0}" == "1" ]]; then
    gate "linter harness (real kube-apiserver + apply)" bash -c 'cd turion-platform-drift && bash scripts/verify.sh'
  else
    skip "linter harness (real kube-apiserver + apply)" "set RUN_CLUSTER=1"
  fi
else
  skip "all Go gates" "go not installed"
fi

# The exact `kubectl apply --dry-run=client` gates need API discovery; they are
# executed by turion-platform-drift/scripts/verify.sh against the kwok apiserver
# when RUN_CLUSTER=1 (and in the no-cluster harness they are intentionally absent).
if have kubectl; then
  gate "kubectl client installed" kubectl version --client
else
  skip "kubectl client installed" "kubectl not installed"
fi

section "3. Docker one-command demo"
if have docker && docker info >/dev/null 2>&1; then
  gate "docker compose build" docker compose build
  gate "plain docker compose up < 45 s" bash -c '
    start=$(date +%s%N)
    docker compose up >/tmp/ctb-compose.log 2>&1
    rc=$?; end=$(date +%s%N); ms=$(( (end-start)/1000000 ))
    cat /tmp/ctb-compose.log
    grep -q "PASS: 15 passed" /tmp/ctb-compose.log || { echo "acceptance PASS line missing"; exit 1; }
    echo "compose up ${ms}ms"
    [[ $rc -eq 0 && $ms -lt 45000 ]]
  '
  docker compose down -v >/dev/null 2>&1 || true
else
  skip "docker compose demo" "docker daemon not available"
fi

section "Summary"
for r in "${RESULTS[@]}"; do echo "$r" | tee -a "$LOG"; done
echo "$PASS passed, $FAIL failed, $SKIP skipped" | tee -a "$LOG"
[[ $FAIL -eq 0 ]]
