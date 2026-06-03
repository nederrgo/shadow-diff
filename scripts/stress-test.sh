#!/usr/bin/env bash
# Phase 4b stress test: Beru TTL eviction, Igris 413 payload guard, high-concurrency hey.
# Requires: kubectl, curl, grpcurl (go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest)
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
STRESS_FLOOD_COUNT="${STRESS_FLOOD_COUNT:-200}"
BERU_PF_PORT="${BERU_PF_PORT:-50051}"
IGRIS_PF_PORT="${IGRIS_PF_PORT:-8888}"
HEY_BIN="${HEY_BIN:-}"
HEY_VERSION="${HEY_VERSION:-v0.1.4}"

export PATH="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin:$PATH"

PF_BERU_PID=""
PF_IGRIS_PID=""

cleanup() {
  [[ -n "$PF_BERU_PID" ]] && kill "$PF_BERU_PID" 2>/dev/null || true
  [[ -n "$PF_IGRIS_PID" ]] && kill "$PF_IGRIS_PID" 2>/dev/null || true
  rm -f /tmp/stress-10mb.json
}
trap cleanup EXIT

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "ERROR: required command not found: $1" >&2
    exit 1
  fi
}

resolve_hey() {
  if [[ -n "$HEY_BIN" && -x "$HEY_BIN" ]]; then
    return 0
  fi
  if command -v hey >/dev/null 2>&1; then
    HEY_BIN="$(command -v hey)"
    return 0
  fi

  local os arch asset url
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "WARN: unsupported arch for hey download: $arch; will use curl loop" >&2; return 1 ;;
  esac
  asset="hey_${os}_${arch}"
  url="https://github.com/rakyll/hey/releases/download/${HEY_VERSION}/${asset}"
  HEY_BIN="/tmp/hey"
  echo "==> Downloading hey from ${url}"
  if curl -fsSL "$url" -o "$HEY_BIN" && chmod +x "$HEY_BIN"; then
    return 0
  fi
  echo "WARN: failed to download hey; will use curl loop fallback" >&2
  return 1
}

start_port_forward() {
  local ns=$1 svc=$2 local_port=$3 remote_port=$4
  if ss -tln 2>/dev/null | grep -q ":${local_port} "; then
    echo "Port ${local_port} already in use (assuming port-forward is running)."
    return 0
  fi
  kubectl port-forward -n "$ns" "svc/${svc}" "${local_port}:${remote_port}" >/dev/null 2>&1 &
  local pid=$!
  sleep 2
  if ! ss -tln 2>/dev/null | grep -q ":${local_port} "; then
    echo "ERROR: port-forward to ${svc}:${remote_port} failed" >&2
    exit 1
  fi
  echo "$pid"
}

require_cmd kubectl
require_cmd curl
require_cmd grpcurl

if ! command -v ss >/dev/null 2>&1; then
  echo "WARN: ss not found; port-forward detection may be limited" >&2
fi

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ -z "$SHADOW_NS" ]]; then
  echo "ERROR: ShadowTest ${SHADOWTEST} not ready (missing shadowNamespace). Apply examples/e2e-shadowtest.yaml first." >&2
  exit 1
fi
echo "Shadow namespace: ${SHADOW_NS}"

IGRIS_URL="${IGRIS_URL:-http://127.0.0.1:${IGRIS_PF_PORT}}"

echo "==> Port-forward Beru gRPC"
PF_BERU_PID=$(start_port_forward beru-system beru "$BERU_PF_PORT" 50051)

echo "==> Port-forward Igris (manual test port ${IGRIS_PF_PORT})"
PF_IGRIS_PID=$(start_port_forward "$SHADOW_NS" "${SHADOWTEST}-igris" "$IGRIS_PF_PORT" "$IGRIS_PF_PORT")

echo ""
echo "==> Test A: Beru unmatched trace flood (${STRESS_FLOOD_COUNT} traces)"
BODY_B64='eyJvayI6dHJ1ZX0='
for i in $(seq 1 "$STRESS_FLOOD_COUNT"); do
  grpcurl -plaintext \
    -import-path "$REPO/beru/api/proto" \
    -proto beru/v1/traffic.proto \
    -d "{\"report\":{\"trace_id\":\"stress-flood-${i}\",\"role\":\"control-a\",\"direction\":\"INGRESS\",\"payload\":{\"content_type\":\"application/json\",\"body\":\"${BODY_B64}\"}}}" \
    "localhost:${BERU_PF_PORT}" beru.v1.TrafficReporter/ReportTraffic >/dev/null
done
echo "Sent ${STRESS_FLOOD_COUNT} incomplete traces (control-a only)."

echo "Waiting for Beru TTL sweeper (default 30s + sweep interval)..."
sleep 35
BERU_LOGS=$(kubectl logs -n beru-system deployment/beru --tail=200 2>/dev/null || true)
if echo "$BERU_LOGS" | grep -q 'Timed out waiting for Trace stress-flood-'; then
  echo "PASS: Beru TTL eviction observed for flood traces"
else
  echo "WARN: did not see TTL timeout logs yet (may need longer wait or lower BERU_TRACE_TTL)" >&2
fi

echo ""
echo "==> Test B: Igris 413 on 10MB payload"
python3 - <<'PY'
import json
payload = {"padding": "x" * (10 * 1024 * 1024 - 30)}
with open("/tmp/stress-10mb.json", "w") as f:
    json.dump(payload, f)
print(f"Wrote {len(json.dumps(payload))} byte JSON payload")
PY

HTTP_CODE=$(curl -sS -o /dev/null -w '%{http_code}' \
  -X POST -H 'Content-Type: application/json' \
  --data-binary @/tmp/stress-10mb.json \
  "${IGRIS_URL}/")
if [[ "$HTTP_CODE" == "413" ]]; then
  echo "PASS: Igris returned 413 for 10MB payload"
else
  echo "FAIL: expected HTTP 413, got ${HTTP_CODE}" >&2
  exit 1
fi

IGRIS_LOGS=$(kubectl logs -n "$SHADOW_NS" "deploy/${SHADOWTEST}-igris" --tail=50 2>/dev/null || true)
if echo "$IGRIS_LOGS" | grep -q 'multicast complete'; then
  echo "WARN: Igris may have multicasted despite oversized body — check logs" >&2
else
  echo "PASS: no recent multicast complete in Igris logs after 413"
fi

echo ""
echo "==> Test C: high-concurrency requests"
if resolve_hey; then
  echo "Using hey: ${HEY_BIN}"
  "$HEY_BIN" -z 5s -c 10 "${IGRIS_URL}/get"
else
  echo "Fallback: bash curl loop (50 requests)"
  for _ in $(seq 1 50); do
    curl -sS -o /dev/null "${IGRIS_URL}/get" || true
  done
fi

echo ""
echo "==> Health check: pods still Running"
kubectl get pods -n beru-system -l app=beru -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\n"}{end}'
kubectl get pods -n "$SHADOW_NS" -l app.kubernetes.io/component=igris -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\n"}{end}' 2>/dev/null \
  || kubectl get pods -n "$SHADOW_NS" | grep igris || true

if command -v kubectl >/dev/null 2>&1 && kubectl top pod -n beru-system 2>/dev/null | grep -q beru; then
  echo ""
  echo "Beru memory (metrics-server):"
  kubectl top pod -n beru-system -l app=beru 2>/dev/null || true
fi

echo ""
echo "Phase 4b stress test complete."
