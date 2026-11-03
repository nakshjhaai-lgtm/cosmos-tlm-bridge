#!/bin/sh
# Entrypoint for the cosmos-tlm-bridge image.
#
#   docker run ... serve      -> DROID_SIM OpenC3 reference server (default)
#   docker run ... demo       -> full self-contained acceptance run
#   docker run ... run <yaml> -> run a suite against $COSMOS_URL
#   docker run ... bridge     -> forward typed frames to $WEBHOOK_URL
#   docker run ... shell      -> interactive shell
set -eu

case "${1:-serve}" in
  serve)
    shift || true
    exec python -m cosmos_tlm_bridge.cli serve --host 0.0.0.0 --port "${COSMOS_PORT:-2900}" "$@"
    ;;
  demo)
    shift || true
    exec python -m cosmos_tlm_bridge.cli demo --port "${COSMOS_PORT:-2900}" "$@"
    ;;
  run)
    shift
    exec python -m cosmos_tlm_bridge.cli run "$@" --cosmos-url "${COSMOS_URL:-http://localhost:2900}"
    ;;
  bridge)
    shift || true
    # PACKETS is a whitespace-separated list; the CLI uses nargs="+".
    packets=${PACKETS:-ADCS EPS THERMAL}
    # shellcheck disable=SC2086
    set -- $packets
    if [ -n "${WEBHOOK_URL:-}" ]; then
      exec python -m cosmos_tlm_bridge.cli bridge \
        --cosmos-url "${COSMOS_URL:-http://localhost:2900}" \
        --packet "$@" \
        --webhook "$WEBHOOK_URL" \
        --duration "${BRIDGE_DURATION:-30}"
    fi
    exec python -m cosmos_tlm_bridge.cli bridge \
      --cosmos-url "${COSMOS_URL:-http://localhost:2900}" \
      --packet "$@" \
      --duration "${BRIDGE_DURATION:-30}"
    ;;
  shell)
    exec /bin/sh
    ;;
  *)
    exec python -m cosmos_tlm_bridge.cli "$@"
    ;;
esac
