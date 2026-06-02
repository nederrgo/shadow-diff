#!/usr/bin/env bash
# E2E: prod egress -> Siphon records -> Beru MockStore -> shadow replay (no manual seed_mock)
#
# Requires Ready ShadowTest with spec.downstreams and running Siphon/Beru.
# Run after ./scripts/e2e-reset-kind.sh or with an existing Ready stack.
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
# shellcheck source=scripts/lib/siphon-config.sh
source "$REPO/scripts/lib/siphon-config.sh"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_DEPLOY="${SHADOW_DEPLOY:-${SHADOWTEST}-control-a}"
PROD_DEPLOY="${PROD_DEPLOY:-my-prod-app}"
PROD_NS="${PROD_NS:-default}"
EGRESS_HOST="${EGRESS_HOST:-}"
EGRESS_PATH="${EGRESS_PATH:-/post}"
EGRESS_PROXY="${EGRESS_PROXY:-http://127.0.0.1:15001}"
RECORD_BODY="${RECORD_BODY:-{\"e2e_record\":1}}"

EGRESS_LAST_CODE=""
EGRESS_LAST_BODY=""

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}
need kubectl

parse_egress_output() {
  local out="$1"
  out=$(echo "$out" | grep -v '^Targeting container' | grep -v "^If you don't see" | grep -v '^Defaulting container')
  local code_line
  code_line=$(echo "$out" | grep -E '__CODE__[0-9]+$' | tail -1 || true)
  if [[ -z "$code_line" ]]; then
    echo "ERROR: could not parse curl output:${out}" >&2
    exit 1
  fi
  EGRESS_LAST_CODE=$(echo "$code_line" | sed -E 's/.*__CODE__([0-9]+)$/\1/')
  # curl -w appends __CODE__ on the same line or (with trailing echo) on its own line.
  local inline_body
  inline_body=$(echo "$code_line" | sed -E 's/__CODE__[0-9]+$//')
  if [[ -n "$inline_body" ]]; then
    EGRESS_LAST_BODY="$inline_body"
  else
    EGRESS_LAST_BODY=$(echo "$out" | awk '/__CODE__[0-9]+$/{exit} {print}')
  fi
}

run_curl_in_pod() {
  local ns="$1"
  local pod="$2"
  local container="$3"
  local curl_script="$4"

  local out=""
  if kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c 'command -v curl >/dev/null' 2>/dev/null; then
    out=$(kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c "$curl_script")
  else
    echo "    (container has no curl — ephemeral debug container on pod network)"
    local dbg="e2e-curl-${RANDOM}"
    out=$(kubectl debug "$pod" -n "$ns" \
      --image=curlimages/curl:latest \
      --target="$container" \
      --container="$dbg" \
      --attach \
      -- sh -c "$curl_script" 2>&1 || true)
  fi
  parse_egress_output "$out"
}

egress_curl() {
  local body="$1"
  local url="http://${EGRESS_HOST}${EGRESS_PATH}"
  local body_q url_q proxy_q
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")
  proxy_q=$(printf '%q' "$EGRESS_PROXY")

  local curl_script
  curl_script=$(cat <<SCRIPT
curl -sS -w '__CODE__%{http_code}' \\
  -x ${proxy_q} \\
  -H 'Content-Type: application/json' \\
  -d ${body_q} \\
  ${url_q}
echo
SCRIPT
)
  run_curl_in_pod "$SHADOW_NS" "$SHADOW_POD" "app" "$curl_script"
}

prod_record_curl() {
  local body="$1"
  local url="http://${EGRESS_HOST}${EGRESS_PATH}"
  local body_q url_q
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")

  local curl_script
  curl_script=$(cat <<SCRIPT
curl -sS -w '__CODE__%{http_code}' \\
  -H 'Content-Type: application/json' \\
  -d ${body_q} \\
  ${url_q}
echo
SCRIPT
)
  run_curl_in_pod "$PROD_NS" "$PROD_POD" "nginx" "$curl_script"
}

echo "==> Record-replay E2E prerequisites"
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  echo "ERROR: ShadowTest must be Ready (phase=$phase shadowNamespace=$SHADOW_NS)" >&2
  exit 1
fi

if [[ -z "$EGRESS_HOST" ]]; then
  EGRESS_HOST=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.downstreams[0].host}' 2>/dev/null || true)
fi
if [[ -z "$EGRESS_HOST" ]]; then
  echo "ERROR: set EGRESS_HOST or add spec.downstreams[0].host to ShadowTest" >&2
  exit 1
fi

kubectl wait -n "$SHADOW_NS" --for=condition=Ready pod \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" --timeout=120s
kubectl wait -n "$PROD_NS" --for=condition=Ready pod -l app=my-prod-app --timeout=120s
kubectl wait -n siphon-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=siphon-agent --timeout=120s
kubectl wait -n beru-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=beru --timeout=120s

SHADOW_POD=$(kubectl get pod -n "$SHADOW_NS" \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
  -o jsonpath='{.items[0].metadata.name}')
PROD_POD=$(kubectl get pod -n "$PROD_NS" -l app=my-prod-app \
  -o jsonpath='{.items[0].metadata.name}')

echo "    prodPod=$PROD_POD shadowPod=$SHADOW_POD downstream=$EGRESS_HOST"

echo "==> Wait for Siphon recorder config (targets + downstreams + beru_http)"
wait_siphon_configured 1

echo "==> Record phase: prod direct egress to ${EGRESS_HOST}${EGRESS_PATH}"
prod_record_curl "$RECORD_BODY"
prod_code=$EGRESS_LAST_CODE
echo "    prod status=$prod_code"
if [[ "$prod_code" != "200" ]]; then
  echo "ERROR: prod egress to httpbin failed with status $prod_code" >&2
  exit 1
fi

echo "    waiting for Siphon to POST record to Beru"
sleep 5

echo "==> Replay phase: poll shadow egress (no seed_mock) until HTTP 200"
deadline=$((SECONDS + 60))
hit_code=""
while [[ $SECONDS -lt $deadline ]]; do
  egress_curl "$RECORD_BODY"
  hit_code=$EGRESS_LAST_CODE
  echo "    shadow status=$hit_code"
  if [[ "$hit_code" == "200" ]]; then
    break
  fi
  if [[ "$hit_code" != "599" ]]; then
    echo "    unexpected status $hit_code, retrying..."
  fi
  sleep 3
done

if [[ "$hit_code" != "200" ]]; then
  echo "ERROR: expected HTTP 200 from auto-recorded mock, got $hit_code after polling" >&2
  echo "       Debug checklist:" >&2
  echo "         kubectl logs -n siphon-system -l app.kubernetes.io/name=siphon-agent --tail=50 | grep -E 'egress|forward|Siphon target'" >&2
  echo "         kubectl logs -n beru-system deploy/beru --tail=30 | grep -E 'record|Regression'" >&2
  echo "         kubectl get pod -n siphon-system -l app.kubernetes.io/name=siphon-agent -o jsonpath='{.items[0].spec.containers[0].image}'" >&2
  echo "         kubectl get pod -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].spec.containers[0].image}'" >&2
  echo "       Siphon must log 'egress forwarder: recorded ...'; Beru must NOT return 404 on /v1/record_egress" >&2
  exit 1
fi

if [[ "$EGRESS_LAST_BODY" != *"e2e_record"* ]]; then
  echo "ERROR: expected recorded httpbin body to contain e2e_record, got: $EGRESS_LAST_BODY" >&2
  exit 1
fi

echo ""
echo "Record-replay E2E passed:"
echo "  1. Prod POST ${EGRESS_HOST}${EGRESS_PATH} captured by Siphon"
echo "  2. Beru auto-seeded via /v1/record_egress (no manual seed_mock)"
echo "  3. Shadow replay via HTTP_PROXY returned HTTP 200"
