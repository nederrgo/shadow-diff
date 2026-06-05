#!/usr/bin/env bash
# Run k6 stress-test.js with kubectl port-forwards to Igris, control-a, and Beru.
#
# Usage:
#   ./run-stress-test.sh                          # DURATION=2m (default in stress-test.js)
#   ./run-stress-test.sh -e DURATION=30s          # pass extra k6 -e flags
#   ./run-stress-test.sh --scenario limit_payload --scenario beru_health -e DURATION=30s
#   SCENARIOS=limit_payload,beru_health ./run-stress-test.sh -e DURATION=30s
#   SKIP_PORT_FORWARD=1 ./run-stress-test.sh      # use existing forwards on 8888/8889/8080
#
set -euo pipefail

DIR="${DIR:-$(cd "$(dirname "$0")" && pwd)}"
REPO="${REPO:-$(cd "$DIR/../../.." && pwd)}"

SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
IGRIS_PF_PORT="${IGRIS_PF_PORT:-8888}"
ORPHAN_PF_PORT="${ORPHAN_PF_PORT:-8889}"
BERU_HTTP_PF_PORT="${BERU_HTTP_PF_PORT:-8080}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
SCENARIOS="${SCENARIOS:-}"

PF_IGRIS_PID=""
PF_ORPHAN_PID=""
PF_BERU_PID=""

K6_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario)
      shift
      [[ $# -gt 0 ]] || { echo "ERROR: --scenario requires a name" >&2; exit 1; }
      if [[ -n "$SCENARIOS" ]]; then
        SCENARIOS="${SCENARIOS},$1"
      else
        SCENARIOS="$1"
      fi
      shift
      ;;
    *)
      K6_ARGS+=("$1")
      shift
      ;;
  esac
done

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

kill_port_listener() {
  local port=$1
  if ! port_listening "$port"; then
    return 0
  fi
  echo "    Stopping stale listener on port ${port}..."
  pkill -f "kubectl port-forward.*:${port}" 2>/dev/null || true
  sleep 1
  if port_listening "$port" && command -v fuser >/dev/null 2>&1; then
    fuser -k "${port}/tcp" 2>/dev/null || true
    sleep 1
  fi
}

start_port_forward() {
  local ns=$1 svc=$2 local_port=$3 remote_port=$4
  # Always replace existing forwards — stale ones are common after pod restarts.
  kill_port_listener "$local_port"
  kubectl port-forward -n "$ns" "svc/${svc}" "${local_port}:${remote_port}" >/dev/null 2>&1 &
  local pid=$!
  local i
  for i in 1 2 3 4 5; do
    sleep 1
    if port_listening "$local_port"; then
      echo "$pid"
      return 0
    fi
  done
  echo "ERROR: port-forward to ${ns}/${svc}:${remote_port} -> localhost:${local_port} failed" >&2
  echo "       Is the E2E stack up? Run: ${REPO}/testing/scripts/e2e-reset-kind.sh" >&2
  exit 1
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
  echo "       Run: ${REPO}/testing/scripts/e2e-reset-kind.sh" >&2
  exit 1
fi
echo "Shadow namespace: ${SHADOW_NS}"

if [[ "$SKIP_PORT_FORWARD" != "1" ]]; then
  echo "==> Port-forward Igris (${IGRIS_PF_PORT})"
  PF_IGRIS_PID=$(start_port_forward "$SHADOW_NS" "${SHADOWTEST}-igris" "$IGRIS_PF_PORT" "$IGRIS_PF_PORT")

  echo "==> Port-forward control-a for orphan traces (${ORPHAN_PF_PORT} -> svc :8888)"
  PF_ORPHAN_PID=$(start_port_forward "$SHADOW_NS" "${SHADOWTEST}-control-a" "$ORPHAN_PF_PORT" "$IGRIS_PF_PORT")

  echo "==> Port-forward Beru HTTP (${BERU_HTTP_PF_PORT})"
  kubectl wait -n beru-system --for=condition=Ready pod \
    -l app.kubernetes.io/name=beru --timeout=60s >/dev/null
  PF_BERU_PID=$(start_port_forward beru-system beru "$BERU_HTTP_PF_PORT" 8080)
else
  echo "SKIP_PORT_FORWARD=1 — assuming localhost:${IGRIS_PF_PORT}, :${ORPHAN_PF_PORT}, :${BERU_HTTP_PF_PORT} are already forwarded."
fi

TARGET_URL="http://127.0.0.1:${IGRIS_PF_PORT}"
ORPHAN_TARGET_URL="http://127.0.0.1:${ORPHAN_PF_PORT}"
BERU_HEALTH_URL="http://127.0.0.1:${BERU_HTTP_PF_PORT}/healthz"

curl_beru_health() {
  curl -sS --connect-timeout 3 -w '\n__HTTP_CODE__%{http_code}' "$BERU_HEALTH_URL" 2>&1 || echo "connection_failed"
}

echo "==> Preflight"
health_out=$(curl_beru_health)
health_code=$(echo "$health_out" | sed -n 's/.*__HTTP_CODE__\([0-9]*\)$/\1/p')
health_body=$(echo "$health_out" | sed '/__HTTP_CODE__/d')

if [[ "$health_code" != "200" && "$SKIP_PORT_FORWARD" != "1" ]]; then
  echo "    Beru /healthz probe failed — retrying port-forward to Beru..."
  kill_port_listener "$BERU_HTTP_PF_PORT"
  [[ -n "$PF_BERU_PID" ]] && kill "$PF_BERU_PID" 2>/dev/null || true
  PF_BERU_PID=$(start_port_forward beru-system beru "$BERU_HTTP_PF_PORT" 8080)
  health_out=$(curl_beru_health)
  health_code=$(echo "$health_out" | sed -n 's/.*__HTTP_CODE__\([0-9]*\)$/\1/p')
  health_body=$(echo "$health_out" | sed '/__HTTP_CODE__/d')
fi

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
  echo "       Tip: after 'kubectl rollout restart deployment/beru', stale port-forwards break." >&2
  echo "            Kill manually: pkill -f 'port-forward.*:8080'" >&2
  exit 1
else
  echo "ERROR: Beru health check failed at ${BERU_HEALTH_URL} (HTTP ${health_code})" >&2
  echo "       body: ${health_body}" >&2
  exit 1
fi

echo ""
echo "==> k6 run"
K6_ENV=(
  -e "TARGET_URL=${TARGET_URL}"
  -e "ORPHAN_TARGET_URL=${ORPHAN_TARGET_URL}"
  -e "BERU_HEALTH_URL=${BERU_HEALTH_URL}"
)
if [[ -n "$SCENARIOS" ]]; then
  echo "    SCENARIOS=${SCENARIOS}"
  K6_ENV+=(-e "SCENARIOS=${SCENARIOS}")
fi
exec k6 run \
  "${K6_ENV[@]}" \
  "${K6_ARGS[@]}" \
  "${DIR}/stress-test.js"
