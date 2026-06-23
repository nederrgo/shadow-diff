#!/usr/bin/env bash
# E2E: Beru SQLite dashboard — inject MATCH and MISMATCH traces, verify API + HTML UI.
#
# Prerequisites:
#   - Kind cluster with Beru deployed (./testing/scripts/e2e-reset-kind.sh)
#   - grpcurl on PATH (go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest)
#   - curl, kubectl, grpcurl (python3 used for JSON if jq is absent)
#
# Usage:
#   ./testing/scripts/e2e-dashboard-test.sh
#   BERU_IMG=beru:dev ./testing/scripts/e2e-dashboard-test.sh
#   SKIP_BERU_BUILD=1 ./testing/scripts/e2e-dashboard-test.sh
#
# On Windows/WSL: run from WSL; ensure kubectl points at Kind and Docker Desktop is up.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

BERU_NS="${BERU_NS:-beru-system}"
BERU_IMG="${BERU_IMG:-beru:dev}"
BERU_HTTP_PORT="${BERU_HTTP_PORT:-8080}"
BERU_GRPC_PORT="${BERU_GRPC_PORT:-50051}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"
SKIP_BERU_DEPLOY="${SKIP_BERU_DEPLOY:-0}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
TRACE_PREFIX="${TRACE_PREFIX:-dash-e2e-$(date +%s)}"
SHADOW_TEST_NAME="${SHADOW_TEST_NAME:-dashboard-e2e}"

PF_HTTP_PID=""
PF_GRPC_PID=""

json_body_b64() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

# json_query FILE PY_EXPR — parse JSON without requiring jq (uses python3).
json_query() {
  local file="$1" expr="$2"
  if command -v jq >/dev/null 2>&1; then
    case "$expr" in
      run_id_by_name)
        jq -r --arg name "${SHADOW_TEST_NAME}" '.[] | select(.Name == $name) | .ID' "$file" | head -1
        ;;
      run_id_first)
        jq -r '.[0].ID // empty' "$file"
        ;;
      match_count)
        jq -r --arg p "$TRACE_PREFIX" '[.[] | select(.TraceID | startswith($p)) | select(.Status == "MATCH")] | length' "$file"
        ;;
      mismatch_count)
        jq -r --arg p "$TRACE_PREFIX" '[.[] | select(.TraceID | startswith($p)) | select(.Status == "MISMATCH")] | length' "$file"
        ;;
      first_mismatch_id)
        jq -r --arg p "$TRACE_PREFIX" '[.[] | select(.TraceID | startswith($p)) | select(.Status == "MISMATCH")][0].ID' "$file"
        ;;
    esac
    return
  fi
  require_cmd python3
  case "$expr" in
    run_id_by_name)
      python3 - "$file" "$SHADOW_TEST_NAME" <<'PY'
import json, sys
path, name = sys.argv[1], sys.argv[2]
runs = json.load(open(path))
for r in runs:
    if r.get("Name") == name:
        print(r.get("ID", ""))
        break
PY
      ;;
    run_id_first)
      python3 - "$file" <<'PY'
import json, sys
runs = json.load(open(sys.argv[1]))
print(runs[0]["ID"] if runs else "")
PY
      ;;
    match_count)
      python3 - "$file" "$TRACE_PREFIX" <<'PY'
import json, sys
traces, prefix = json.load(open(sys.argv[1])), sys.argv[2]
print(sum(1 for t in traces if t.get("TraceID", "").startswith(prefix) and t.get("Status") == "MATCH"))
PY
      ;;
    mismatch_count)
      python3 - "$file" "$TRACE_PREFIX" <<'PY'
import json, sys
traces, prefix = json.load(open(sys.argv[1])), sys.argv[2]
print(sum(1 for t in traces if t.get("TraceID", "").startswith(prefix) and t.get("Status") == "MISMATCH"))
PY
      ;;
    first_mismatch_id)
      python3 - "$file" "$TRACE_PREFIX" <<'PY'
import json, sys
traces, prefix = json.load(open(sys.argv[1])), sys.argv[2]
for t in traces:
    if t.get("TraceID", "").startswith(prefix) and t.get("Status") == "MISMATCH":
        print(t.get("ID", ""))
        break
PY
      ;;
  esac
}

start_port_forward() {
  local ns="$1" svc="$2" local_port="$3" remote_port="$4"
  if ss -tln 2>/dev/null | grep -q ":${local_port} "; then
    echo "    port ${local_port} already in use (assuming port-forward active)" >&2
    echo ""
    return 0
  fi
  kubectl port-forward -n "$ns" "svc/${svc}" "${local_port}:${remote_port}" >/dev/null 2>&1 &
  echo $!
}

wait_http() {
  local url="$1" max="${2:-30}"
  local i=0
  while [[ "$i" -lt "$max" ]]; do
    if curl -sf --connect-timeout 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

send_ingress_trace() {
  local trace_id="$1" candidate_price="$2"
  local body_a body_b body_c body_a_b64 body_b_b64 body_c_b64
  body_a='{"price":10,"timestamp":1}'
  body_b='{"price":10,"timestamp":2}'
  body_c="{\"price\":${candidate_price},\"timestamp\":1}"
  body_a_b64=$(json_body_b64 "$body_a")
  body_b_b64=$(json_body_b64 "$body_b")
  body_c_b64=$(json_body_b64 "$body_c")

  for spec in "control-a:${body_a_b64}" "control-b:${body_b_b64}" "candidate:${body_c_b64}"; do
    local role="${spec%%:*}"
    local body="${spec##*:}"
    grpcurl -plaintext \
      -import-path "$REPO/pipeline/beru/api/proto" \
      -proto beru/v1/traffic.proto \
      -d "{\"report\":{\"trace_id\":\"${trace_id}\",\"role\":\"${role}\",\"direction\":\"INGRESS\",\"payload\":{\"content_type\":\"application/json\",\"body\":\"${body}\",\"metadata\":{\"shadow_test_name\":\"${SHADOW_TEST_NAME}\"}}}}" \
      "localhost:${BERU_GRPC_PORT}" beru.v1.TrafficReporter/ReportTraffic >/dev/null
  done
}

send_egress_trace() {
  local trace_id="$1" candidate_value="$2"
  local payload_a payload_b payload_c
  payload_a='{"order_id":1,"amount":100}'
  payload_b='{"order_id":1,"amount":100}'
  payload_c="{\"order_id\":1,\"amount\":${candidate_value}}"

  for spec in "control-a:${payload_a}" "control-b:${payload_b}" "candidate:${payload_c}"; do
    local workload="${spec%%:*}"
    local payload="${spec##*:}"
    curl -sf -X POST "http://127.0.0.1:${BERU_HTTP_PORT}/api/v1/egress/diff" \
      -H "Content-Type: application/json" \
      -d "{\"trace_id\":\"${trace_id}\",\"workload\":\"${workload}\",\"protocol\":\"rabbitmq\",\"shadow_test_name\":\"${SHADOW_TEST_NAME}\",\"payload\":${payload}}" >/dev/null
  done
}

cleanup() {
  [[ -n "$PF_HTTP_PID" ]] && kill "$PF_HTTP_PID" 2>/dev/null || true
  [[ -n "$PF_GRPC_PID" ]] && kill "$PF_GRPC_PID" 2>/dev/null || true
}
trap cleanup EXIT
trap '[[ $? -ne 0 ]] && log_fail "Dashboard E2E failed (prefix '"${TRACE_PREFIX}"')"' EXIT

require_cmd kubectl
require_cmd curl
require_cmd grpcurl
if ! command -v jq >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1; then
  log_fail "need jq or python3 for JSON parsing (e.g. sudo apt-get install -y jq)"
  exit 1
fi
require_kubectl_cluster

echo "==> Beru dashboard E2E (trace prefix ${TRACE_PREFIX})"

kubectl get deploy -n "$BERU_NS" beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed in ${BERU_NS} — run: ./testing/scripts/e2e-reset-kind.sh"
  exit 1
}

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  echo "==> Build Beru image"
  make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
fi

if [[ "$SKIP_BERU_DEPLOY" != "1" ]]; then
  if command -v kind >/dev/null 2>&1 && [[ -n "$KIND_CLUSTER" ]]; then
    kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER" 2>/dev/null || true
  fi
  kubectl set image deployment/beru -n "$BERU_NS" beru="$BERU_IMG" --record=false 2>/dev/null || true
  kubectl rollout status deployment/beru -n "$BERU_NS" --timeout=120s
fi

echo "==> Port-forward Beru HTTP :${BERU_HTTP_PORT} and gRPC :${BERU_GRPC_PORT}"
PF_HTTP_PID=$(start_port_forward "$BERU_NS" beru "$BERU_HTTP_PORT" 8080)
PF_GRPC_PID=$(start_port_forward "$BERU_NS" beru "$BERU_GRPC_PORT" 50051)
sleep 2

BERU_HTTP="http://127.0.0.1:${BERU_HTTP_PORT}"
wait_http "${BERU_HTTP}/healthz" 30 || {
  log_fail "Beru HTTP health check failed at ${BERU_HTTP}/healthz"
  exit 1
}
log_success "Beru HTTP reachable"

echo "==> Inject traces (2 MATCH ingress, 2 MISMATCH ingress, 1 MATCH egress, 1 MISMATCH egress)"
send_ingress_trace "${TRACE_PREFIX}-match-1" 10
send_ingress_trace "${TRACE_PREFIX}-mismatch-1" 12
send_ingress_trace "${TRACE_PREFIX}-match-2" 10
send_ingress_trace "${TRACE_PREFIX}-mismatch-2" 99
send_egress_trace "${TRACE_PREFIX}-egress-match" 100
send_egress_trace "${TRACE_PREFIX}-egress-mismatch" 200
sleep 2

echo "==> Verify dashboard HTML"
for path in /dashboard/ /; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "${BERU_HTTP}${path}")
  if [[ "$path" == "/" ]]; then
    [[ "$code" == "307" || "$code" == "302" ]] || { log_fail "GET / expected redirect, got ${code}"; exit 1; }
  else
    [[ "$code" == "200" ]] || { log_fail "GET ${path} expected 200, got ${code}"; exit 1; }
  fi
done
log_success "dashboard pages respond"

echo "==> Verify shadow test run API"
runs_json=$(curl -sf "${BERU_HTTP}/api/v1/shadow-tests")
runs_file=$(mktemp)
traces_file=$(mktemp)
trap 'rm -f "$runs_file" "$traces_file"; cleanup' EXIT
echo "$runs_json" >"$runs_file"
run_id=$(json_query "$runs_file" run_id_by_name)
if [[ -z "$run_id" || "$run_id" == "null" ]]; then
  run_id=$(json_query "$runs_file" run_id_first)
fi
[[ -n "$run_id" ]] || { log_fail "no shadow test run in API response: $runs_json"; exit 1; }
log_success "shadow test run id=${run_id}"

echo "==> Verify trace counts via API"
traces_json=$(curl -sf "${BERU_HTTP}/api/v1/traces?shadow_test_id=${run_id}")
echo "$traces_json" >"$traces_file"
match_count=$(json_query "$traces_file" match_count)
mismatch_count=$(json_query "$traces_file" mismatch_count)

echo "    MATCH traces (this run):    ${match_count}"
echo "    MISMATCH traces (this run): ${mismatch_count}"

if [[ "${match_count:-0}" -lt 2 ]]; then
  log_fail "expected at least 2 MATCH traces, got ${match_count:-0}"
  echo "$traces_json" >&2
  exit 1
fi
if [[ "${mismatch_count:-0}" -lt 2 ]]; then
  log_fail "expected at least 2 MISMATCH traces, got ${mismatch_count:-0}"
  echo "$traces_json" >&2
  exit 1
fi
log_success "found ${match_count} MATCH and ${mismatch_count} MISMATCH traces"

echo "==> Verify mismatch diff viewer"
mismatch_id=$(json_query "$traces_file" first_mismatch_id)
[[ -n "$mismatch_id" && "$mismatch_id" != "null" ]] || { log_fail "no mismatch trace id"; exit 1; }
diff_code=$(curl -sS -o /dev/null -w '%{http_code}' "${BERU_HTTP}/dashboard/traces/${mismatch_id}")
[[ "$diff_code" == "200" ]] || { log_fail "GET /dashboard/traces/${mismatch_id} got ${diff_code}"; exit 1; }
log_success "diff viewer page OK for trace id=${mismatch_id}"

echo "==> Verify noise filter API"
filter_code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BERU_HTTP}/api/v1/noise/filters" \
  -H "Content-Type: application/json" \
  -d "{\"shadow_test_name\":\"${SHADOW_TEST_NAME}\",\"path\":\"price\"}")
[[ "$filter_code" == "201" ]] || { log_fail "POST /api/v1/noise/filters expected 201, got ${filter_code}"; exit 1; }
log_success "noise filter saved"

echo ""
log_success "Dashboard E2E complete"
echo "    Open: ${BERU_HTTP}/dashboard/"
echo "    Traces prefixed: ${TRACE_PREFIX}-*"
echo "    Shadow test name: ${SHADOW_TEST_NAME}"
