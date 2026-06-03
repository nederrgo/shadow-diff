#!/usr/bin/env bash
# Run k6 stress-test.js with kubectl port-forwards to Igris, control-a, and Beru.
#
# Usage:
#   ./run-stress-test.sh                          # DURATION=2m (default in stress-test.js)
#   ./run-stress-test.sh -e DURATION=30s          # pass extra k6 -e flags
#   SKIP_PORT_FORWARD=1 ./run-stress-test.sh      # use existing forwards on 8888/8889/8080
#
set -euo pipefail

DIR="${DIR:-$(cd "$(dirname "$0")" && pwd)}"
REPO="${REPO:-$(cd "$DIR/../.." && pwd)}"

SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
IGRIS_PF_PORT="${IGRIS_PF_PORT:-8888}"
ORPHAN_PF_PORT="${ORPHAN_PF_PORT:-8889}"
BERU_HTTP_PF_PORT="${BERU_HTTP_PF_PORT:-8080}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"

PF_IGRIS_PID=""
PF_ORPHAN_PID=""
PF_BERU_PID=""

cleanup() {
  [[ -n "$PF_IGRIS_PID" ]] && kill "$PF_IGRIS_PID" 2>/dev/null || true
  [[ -n "$PF_ORPHAN_PID" ]] && kill "$PF_ORPHAN_PID" 2>/dev/null || true
  [[ -n "$PF_BERU_PID" ]] && kill "$PF_BERU_PID" 2>/dev/null || true
}
trap cleanup EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: required command not found: $1" >&2
    exit 1
  }
}

port_listening() {
  local port=$1
  if command -v ss >/dev/null 2>&1; then
    ss -tln | grep -q ":${port} "
    return
  fi
  if command -v netstat >/dev/null 2>&1; then
    netstat -tln 2>/dev/null | grep -q ":${port} "
    return
  fi
  return 1
}

start_port_forward() {
  local ns=$1 svc=$2 local_port=$3 remote_port=$4
  if port_listening "$local_port"; then
    echo "Port ${local_port} already listening (skipping port-forward for ${svc})."
    echo ""
    return 0
  fi
  kubectl port-forward -n "$ns" "svc/${svc}" "${local_port}:${remote_port}" >/dev/null 2>&1 &
  local pid=$!
  sleep 2
  if ! port_listening "$local_port"; then
    echo "ERROR: port-forward to ${ns}/${svc}:${remote_port} -> localhost:${local_port} failed" >&2
    echo "       Is the E2E stack up? Run: ${REPO}/scripts/e2e-reset-kind.sh" >&2
    exit 1
  fi
  echo "$pid"
}

require_cmd kubectl
require_cmd k6
require_cmd curl

if ! kubectl cluster-info >/dev/null 2>&1; then
  echo "ERROR: kubectl cannot reach a cluster. Check kubeconfig / Kind cluster." >&2
  exit 1
fi

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ -z "$SHADOW_NS" ]]; then
  echo "ERROR: ShadowTest ${SHADOWTEST} not Ready (missing shadowNamespace)." >&2
  echo "       Run: ${REPO}/scripts/e2e-reset-kind.sh" >&2
  exit 1
fi
echo "Shadow namespace: ${SHADOW_NS}"

if [[ "$SKIP_PORT_FORWARD" != "1" ]]; then
  echo "==> Port-forward Igris (${IGRIS_PF_PORT})"
  PF_IGRIS_PID=$(start_port_forward "$SHADOW_NS" "${SHADOWTEST}-igris" "$IGRIS_PF_PORT" "$IGRIS_PF_PORT")

  echo "==> Port-forward control-a for orphan traces (${ORPHAN_PF_PORT} -> svc :8888)"
  PF_ORPHAN_PID=$(start_port_forward "$SHADOW_NS" "${SHADOWTEST}-control-a" "$ORPHAN_PF_PORT" "$IGRIS_PF_PORT")

  echo "==> Port-forward Beru HTTP (${BERU_HTTP_PF_PORT})"
  PF_BERU_PID=$(start_port_forward beru-system beru "$BERU_HTTP_PF_PORT" 8080)
else
  echo "SKIP_PORT_FORWARD=1 — assuming localhost:${IGRIS_PF_PORT}, :${ORPHAN_PF_PORT}, :${BERU_HTTP_PF_PORT} are already forwarded."
fi

TARGET_URL="http://127.0.0.1:${IGRIS_PF_PORT}"
ORPHAN_TARGET_URL="http://127.0.0.1:${ORPHAN_PF_PORT}"
BERU_HEALTH_URL="http://127.0.0.1:${BERU_HTTP_PF_PORT}/healthz"

echo "==> Preflight"
health_out=$(curl -sS --connect-timeout 3 -w '\n__HTTP_CODE__%{http_code}' "$BERU_HEALTH_URL" 2>&1) || health_out="connection_failed"
health_code=$(echo "$health_out" | sed -n 's/.*__HTTP_CODE__\([0-9]*\)$/\1/p')
health_body=$(echo "$health_out" | sed '/__HTTP_CODE__/d')

if [[ "$health_code" == "200" ]]; then
  echo "    Beru /healthz OK"
elif [[ "$health_code" == "404" ]]; then
  echo "ERROR: Beru returned HTTP 404 for ${BERU_HEALTH_URL}" >&2
  echo "       The running pod is an old image (no /healthz). Rebuild is not enough — reload into Kind and restart:" >&2
  echo "         make beru-docker-build BERU_IMG=beru:dev" >&2
  echo "         kind load docker-image beru:dev --name \$(kind get clusters | head -1)" >&2
  echo "         kubectl rollout restart deployment/beru -n beru-system" >&2
  echo "         kubectl rollout status deployment/beru -n beru-system" >&2
  exit 1
elif [[ "$health_code" == "" ]]; then
  echo "ERROR: Beru health check failed at ${BERU_HEALTH_URL} (connection refused or timeout)" >&2
  echo "       curl output: ${health_out}" >&2
  exit 1
else
  echo "ERROR: Beru health check failed at ${BERU_HEALTH_URL} (HTTP ${health_code})" >&2
  echo "       body: ${health_body}" >&2
  exit 1
fi

echo ""
echo "==> k6 run"
exec k6 run \
  -e "TARGET_URL=${TARGET_URL}" \
  -e "ORPHAN_TARGET_URL=${ORPHAN_TARGET_URL}" \
  -e "BERU_HEALTH_URL=${BERU_HEALTH_URL}" \
  "$@" \
  "${DIR}/stress-test.js"
