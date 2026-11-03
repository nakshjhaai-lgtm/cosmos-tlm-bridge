# ENVIRONMENT

What was installed to build and verify this archive, how to rebuild that
environment from nothing, and what each project actually needs (very little).

Both projects deliberately avoid heavyweight dependencies: project 1 needs only
`pydantic` + `PyYAML`, project 2 needs only the Go standard library. Everything
below is *tooling used to verify*, not a runtime requirement.

---

## 1. Versions actually used (2026-11-03)

| Component | Version | Where it came from |
| --- | --- | --- |
| Python | 3.13.16 (project supports ≥ 3.11) | image default `/usr/local/bin/python3` |
| pydantic | 2.14.2 | PyPI |
| PyYAML | 6.x | PyPI |
| pytest | 8.x | PyPI |
| Go | 1.23.4 | `https://go.dev/dl/go1.23.4.linux-amd64.tar.gz` |
| kubectl | stable (v1.36-era client) | auto-installed by `scripts/kwok-cluster.sh` from `dl.k8s.io` |
| kwokctl | v0.8.0 | auto-installed by `scripts/kwok-cluster.sh` from GitHub releases |
| helm | v3.16.3 | auto-installed by `scripts/install-crds.sh` from `get.helm.sh` |
| kube-apiserver | **v1.36.1** | provided by kwok (real upstream binaries) |
| Crossplane CRDs | **v2.4.3** | `raw.githubusercontent.com/crossplane/crossplane/v2.4.3/cluster/crds/*` |
| provider-minio CRDs | **v0.4.4** | `raw.githubusercontent.com/vshn/provider-minio/v0.4.4/package/crds/*` |
| CloudNativePG / External Secrets / OpenTelemetry Operator CRDs | current chart releases | Helm repos `cnpg`, `external-secrets`, `open-telemetry` |
| Docker | **not available** — hence the Docker assets are unproven | — |

Total CRDs installed in the verification cluster: **45**.

---

## 2. Rebuild from nothing (project 1)

```bash
# Python 3.11+ is all you need; there is no system dependency.
cd PROJECT-1-cosmos-tlm-bridge

python3 -m venv .venv
.venv/bin/pip install -e ".[dev]"        # pydantic, PyYAML, pytest, ruff, mypy
.venv/bin/python -m pytest               # 80 passed
.venv/bin/cosmos-tlm-bridge demo         # 12/12 checks, ~23 s
```

If you prefer `uv` (as the Makefile assumes):

```bash
uv venv --python 3.11 && uv pip install -e ".[dev]"
```

Build the wheel:

```bash
.venv/bin/pip install build && .venv/bin/python -m build --wheel --outdir release
```

Go client/bridge (optional, no third-party modules):

```bash
cd go && go test ./...
```

---

## 3. Rebuild from nothing (project 2)

```bash
cd PROJECT-2-turion-platform-drift

# Go 1.22+. If the machine has no Go:
#   curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz | tar -C /usr/local -xzf -
#   export PATH=/usr/local/go/bin:$PATH

go build -o /tmp/tpd . && go test ./...          # 99 tests
cd /tmp && /tmp/tpd scan <path-to>/ground-service    # scorecard, no cluster needed
```

Everything except the cluster gates works offline. For the full 32-gate run
(needs network the first time, to fetch kubectl/kwokctl/helm/CRDs):

```bash
bash scripts/verify.sh           # 32 gates, ~92 s
bash scripts/verify.sh --no-cluster   # 23 gates, no API server
```

Environment variables the harness understands:

| Variable | Default | Purpose |
| --- | --- | --- |
| `KWOK_CLUSTER` | `ground` | cluster name under `~/.kwok/clusters/` |
| `KUBECONFIG` | set by the harness | kubeconfig for the gates |
| `BIN_DIR` | `~/.local/bin` | where kubectl/kwokctl/helm are installed |
| `KWOK_VERSION` | `v0.8.0` | kwokctl release |
| `HELM_VERSION` | `v3.16.3` | helm release |
| `CROSSPLANE_VERSION` | `v2.4.3` | CRD source |
| `PROVIDER_MINIO_VERSION` | `v0.4.4` | CRD source |
| `TPD_NAMESPACE` | `ground-system` | namespace pre-created for the server dry-run gate |

Manual cluster control:

```bash
bash scripts/kwok-cluster.sh up ground
export KUBECONFIG=$HOME/.kwok/clusters/ground/kubeconfig.yaml
kubectl get nodes            # kwok fabricates ready nodes
bash scripts/install-crds.sh # 45 CRDs + served-version dump
bash scripts/kwok-cluster.sh down ground
```

---

## 4. Sandbox / CI quirks worth knowing

| Symptom | Cause | Fix |
| --- | --- | --- |
| `go: command not found` | Go is not on the image | install to `/usr/local/go` and add it to `PATH` |
| `uv: command not found` | not installed | use `python3 -m venv` + `pip` |
| kube-apiserver exits immediately | sandbox has no default route | `kwok-cluster.sh` passes `--extra-args kube-apiserver=advertise-address=127.0.0.1` |
| `Permission denied` on `scripts/*.sh` | archive/checkout dropped the executable bit | invoke with `bash scripts/...` (the harness does); modes are tracked as `100755` in git |
| `namespaces "ground-system" not found` during `--dry-run=server` | server-side dry-run does not persist the bundle's own Namespace | create the namespace first (the gate does) |
| Helm warnings pollute the manifest stream | `helm` writes to stderr | the validator captures stdout and stderr separately (regression-tested) |
| `dist/`, `build/`, `out/` disappear | those directory names are excluded from workspace snapshots | the wheel is kept in `release/` |
| Old cluster keeps failing a dry-run | leftover objects from a previous run | `bash scripts/kwok-cluster.sh down ground && rm -rf ~/.kwok` |

---

## 5. What each project needs at runtime (the honest minimum)

| | Project 1 | Project 2 |
| --- | --- | --- |
| Language runtime | Python ≥ 3.11 (`pydantic`, `PyYAML`) | none — a static Go binary |
| Optional | Go (for the Go client), Docker (for the compose stack) | `python3` for the richest Python analysis (falls back to regex with `--no-python`), `kubectl`/`helm` for the strongest validation gates |
| Network | none (mock server is in-process) | none for `scan`/`generate`; first run of `verify.sh` fetches tools + CRDs |
| Credentials | none — keyless by design | none |
| Cluster | none | optional; a real API server only for the server-side gates |
