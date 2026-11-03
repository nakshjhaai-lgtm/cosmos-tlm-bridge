#!/usr/bin/env bash
# Create a real Kubernetes API server with no container runtime and no cloud.
#
# The Definition of Done requires `kubectl apply --dry-run=client`, which in a
# modern kubectl validates against the *server's* OpenAPI. That means a cluster
# has to exist. kwok's "binary" runtime gives us etcd + kube-apiserver +
# controller-manager + scheduler as plain processes: fast, disposable, and
# usable in CI on a machine with no Docker.
#
# Usage: scripts/kwok-cluster.sh [up|down|status] [cluster-name]
set -euo pipefail

ACTION="${1:-up}"
CLUSTER="${2:-ground}"
KWOK_VERSION="${KWOK_VERSION:-v0.8.0}"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"

mkdir -p "$BIN_DIR"
export PATH="$BIN_DIR:$PATH"

install_kwokctl() {
  if command -v kwokctl >/dev/null 2>&1; then
    return
  fi
  echo "installing kwokctl $KWOK_VERSION into $BIN_DIR"
  curl -fsSL -o "$BIN_DIR/kwokctl" \
    "https://github.com/kubernetes-sigs/kwok/releases/download/${KWOK_VERSION}/kwokctl-linux-amd64"
  chmod +x "$BIN_DIR/kwokctl"
}

install_kubectl() {
  if command -v kubectl >/dev/null 2>&1; then
    return
  fi
  local ver
  ver="$(curl -fsSL https://dl.k8s.io/release/stable.txt)"
  echo "installing kubectl $ver into $BIN_DIR"
  curl -fsSL -o "$BIN_DIR/kubectl" "https://dl.k8s.io/release/${ver}/bin/linux/amd64/kubectl"
  chmod +x "$BIN_DIR/kubectl"
}

wait_ready() {
  local kubeconfig="$1"
  for _ in $(seq 1 60); do
    if KUBECONFIG="$kubeconfig" kubectl get --raw /healthz >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "cluster did not become ready" >&2
  return 1
}

case "$ACTION" in
  up)
    install_kwokctl
    install_kubectl
    if kwokctl get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
      echo "cluster $CLUSTER already exists"
    else
      # --advertise-address is mandatory in sandboxes with no default route:
      # without it kube-apiserver dies with "unable to select an IP from
      # default routes".
      kwokctl create cluster --name "$CLUSTER" --runtime binary \
        --extra-args kube-apiserver=advertise-address=127.0.0.1 \
        --extra-args kube-apiserver=service-cluster-ip-range=10.0.0.0/24 \
        --wait 120s
    fi
    KUBECONFIG="$HOME/.kwok/clusters/$CLUSTER/kubeconfig.yaml" wait_ready \
      "$HOME/.kwok/clusters/$CLUSTER/kubeconfig.yaml"
    echo "kubeconfig: $HOME/.kwok/clusters/$CLUSTER/kubeconfig.yaml"
    ;;
  down)
    install_kwokctl
    kwokctl delete cluster --name "$CLUSTER" || true
    ;;
  status)
    KUBECONFIG="$HOME/.kwok/clusters/$CLUSTER/kubeconfig.yaml" kubectl get --raw /healthz
    echo
    ;;
  *)
    echo "usage: $0 [up|down|status] [cluster-name]" >&2
    exit 2
    ;;
esac
