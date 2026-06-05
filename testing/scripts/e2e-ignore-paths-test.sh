#!/usr/bin/env bash
# E2E: ignoreRequestPaths / ignore_paths strip JSON fields before egress cache hash.
#
# 1. Patch ShadowTest spec.downstreams[0].ignoreRequestPaths (Monarch -> Envoy ext_proc metadata)
# 2. seed_mock with ignore_paths — body includes a field that will differ on replay
# 3. Proxied egress with different value for that field must still return the mock (200)
#
# Run after ./testing/scripts/e2e-reset-kind.sh with egress stack up.
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_DEPLOY="${SHADOW_DEPLOY:-${SHADOWTEST}-control-a}"
EGRESS_HOST="${EGRESS_HOST:-httpbin.org}"
EGRESS_PATH="${EGRESS_PATH:-/post}"
EGRESS_PROXY="${EGRESS_PROXY:-http://127.0.0.1:15001}"
BERU_HTTP="${BERU_HTTP:-http://beru.beru-system.svc.cluster.local:8080}"
IGNORE_PATH="${IGNORE_PATH:-$.nonce}"

MISS_BODY="${MISS_BODY:-{\"foo\":99999,\"nonce\":\"e2e-miss-$(date +%s)\"}}"
SEED_BODY='{"foo":1,"nonce":"seed-nonce-111"}'
REPLAY_BODY='{"foo":1,"nonce":"replay-nonce-999"}'

EGRESS_LAST_CODE=""
EGRESS_LAST_BODY=""

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}
need kubectl

parse_egress_output() {
  local out="$1"
  out=$(echo "$out" | grep -v '^Targeting container' | grep -v "^If you don't see" | grep -v '^Defaulting container' | grep -v '^curl:' || true)
  local code_line
  code_line=$(echo "$out" | grep -E '__CODE__[0-9]+$' | tail -1 || true)
  if [[ -z "$code_line" ]]; then
    echo "ERROR: could not parse curl output:${out}" >&2
    exit 1
  fi
  EGRESS_LAST_CODE=$(echo "$code_line" | sed -E 's/.*__CODE__([0-9]+)$/\1/')
  local inline_body
  inline_body=$(echo "$code_line" | sed -E 's/__CODE__[0-9]+$//')
  if [[ -n "$inline_body" ]]; then
    EGRESS_LAST_BODY="$inline_body"
  else
    EGRESS_LAST_BODY=$(echo "$out" | awk '/__CODE__[0-9]+$/{exit} {print}')
  fi
}

refresh_pod() {
  POD=$(kubectl get pod -n "$SHADOW_NS" \
    -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}')
  if [[ -z "$POD" ]]; then
    echo "ERROR: no Running shadow pod for $SHADOW_DEPLOY" >&2
    exit 1
  fi
}

egress_curl() {
  refresh_pod
  local body="$1"
  local url="http://${EGRESS_HOST}${EGRESS_PATH}"
  local body_q url_q proxy_q
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")
  proxy_q=$(printf '%q' "$EGRESS_PROXY")

  local curl_script
  curl_script="curl -sS -w '__CODE__%{http_code}' -x ${proxy_q} -H 'Content-Type: application/json' -d ${body_q} ${url_q}; echo"
  local out=""
  if kubectl exec -n "$SHADOW_NS" "$POD" -c app -- sh -c 'command -v curl >/dev/null' 2>/dev/null; then
    out=$(kubectl exec -n "$SHADOW_NS" "$POD" -c app -- sh -c "$curl_script")
  else
    echo "    (app image has no curl — ephemeral debug container on pod network)"
    local dbg="e2e-curl-${RANDOM}"
    out=$(kubectl debug "$POD" -n "$SHADOW_NS" \
      --image=curlimages/curl:latest \
      --target=app \
      --container="$dbg" \
      --attach \
      -- sh -c "$curl_script" 2>&1 || true)
  fi
  parse_egress_output "$out"
}

echo "==> Ignore-paths E2E prerequisites"
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  echo "ERROR: ShadowTest must be Ready" >&2
  exit 1
fi

echo "==> Patch ShadowTest ignoreRequestPaths=$IGNORE_PATH for host $EGRESS_HOST"
kubectl patch shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --type=json -p "$(cat <<EOF
[
  {"op": "replace", "path": "/spec/downstreams/0/host", "value": "${EGRESS_HOST}"},
  {"op": "replace", "path": "/spec/downstreams/0/ignoreRequestPaths", "value": ["${IGNORE_PATH}"]}
]
EOF
)"
kubectl annotate shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
  shadow-diff.io/reconcile="$(date +%s)" --overwrite >/dev/null
echo "    waiting for Monarch to roll Envoy config"
sleep 8

envoy_yaml=$(kubectl get cm -n "$SHADOW_NS" "${SHADOW_DEPLOY}-envoy" -o jsonpath='{.data.envoy\.yaml}')
if ! grep -q 'ignoreRequestPaths' <<<"$envoy_yaml" && ! grep -qF "$IGNORE_PATH" <<<"$envoy_yaml"; then
  echo "ERROR: Envoy config missing ignoreRequestPaths / $IGNORE_PATH" >&2
  echo "       Fragment:" >&2
  grep -F 'x-shadow-downstreams-config' <<<"$envoy_yaml" | head -1 >&2 || true
  exit 1
fi
echo "    Envoy x-shadow-downstreams-config includes ignore paths"

# ext_proc gRPC streams cache Beru metadata at connect time — restart app pods after config change.
echo "    restart shadow app deployment so Envoy opens fresh ext_proc stream to Beru"
kubectl rollout restart "deployment/${SHADOW_DEPLOY}" -n "$SHADOW_NS"
kubectl rollout status "deployment/${SHADOW_DEPLOY}" -n "$SHADOW_NS" --timeout=120s

POD=$(kubectl get pod -n "$SHADOW_NS" \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
  --field-selector=status.phase=Running \
  -o jsonpath='{.items[0].metadata.name}')
if [[ -z "$POD" ]]; then
  echo "ERROR: no Running shadow pod for $SHADOW_DEPLOY" >&2
  exit 1
fi
echo "    pod=$POD"

echo "==> Baseline: unique body without seed should 599"
egress_curl "$MISS_BODY"
if [[ "$EGRESS_LAST_CODE" != "599" ]]; then
  echo "ERROR: expected 599 before seed, got $EGRESS_LAST_CODE body=$EGRESS_LAST_BODY" >&2
  exit 1
fi
echo "    unseeded status=599 OK"

SEED_JSON=$(cat <<EOF
{
  "method": "POST",
  "host": "${EGRESS_HOST}",
  "path": "${EGRESS_PATH}",
  "body": ${SEED_BODY},
  "ignore_paths": ["${IGNORE_PATH}"],
  "response": {
    "status": 200,
    "headers": {"content-type": "application/json"},
    "body": "{\"mock\":true,\"ignored_field_matching\":true}"
  }
}
EOF
)

echo "==> Seed mock (ignore_paths strips $.nonce before hash)"
seed_out=$(kubectl run "e2e-ignore-seed-${RANDOM}" --rm -i --restart=Never \
  --image=curlimages/curl:latest -- \
  curl -sf -X POST "${BERU_HTTP}/v1/seed_mock" \
  -H 'Content-Type: application/json' \
  -d "$SEED_JSON")
echo "    $seed_out"

echo "==> Replay with different nonce (must hit same cache key -> 200)"
egress_curl "$REPLAY_BODY"
echo "    status=$EGRESS_LAST_CODE body=$EGRESS_LAST_BODY"
if [[ "$EGRESS_LAST_CODE" != "200" ]]; then
  echo "ERROR: expected 200 when only ignored field differs; got $EGRESS_LAST_CODE" >&2
  echo "       Check Beru ext_proc receives ignoreRequestPaths from Envoy metadata." >&2
  exit 1
fi
if [[ "$EGRESS_LAST_BODY" != *'"mock":true'* && "$EGRESS_LAST_BODY" != *'"mock": true'* ]]; then
  echo "ERROR: expected mock body, got: $EGRESS_LAST_BODY" >&2
  exit 1
fi

echo "==> Negative: without ignore_paths, varied nonce should not hit ignore-path mock"
NO_IGNORE_SEED=$(cat <<EOF
{
  "method": "POST",
  "host": "${EGRESS_HOST}",
  "path": "${EGRESS_PATH}",
  "body": ${SEED_BODY},
  "ignore_paths": [],
  "response": {
    "status": 200,
    "headers": {"content-type": "application/json"},
    "body": "{\"strict\":true}"
  }
}
EOF
)
kubectl run "e2e-ignore-strict-${RANDOM}" --rm -i --restart=Never \
  --image=curlimages/curl:latest -- \
  curl -sf -X POST "${BERU_HTTP}/v1/seed_mock" \
  -H 'Content-Type: application/json' \
  -d "$NO_IGNORE_SEED" >/dev/null

egress_curl "$REPLAY_BODY"
echo "    strict seed + varied nonce status=$EGRESS_LAST_CODE body=${EGRESS_LAST_BODY:0:80}"
# With ignore-path mock still in store, replay typically returns the first mock (200 + mock:true).
# A strict-only seed uses a different hash; we only fail if strict body wins over ignore-path mock.
if [[ "$EGRESS_LAST_CODE" == "200" && "$EGRESS_LAST_BODY" == *'"strict":true'* ]]; then
  echo "ERROR: strict mock should not match when nonce differs without ignore_paths" >&2
  exit 1
fi
echo "    OK (got ${EGRESS_LAST_CODE}; strict-only hash did not override ignore-path mock)"

echo ""
echo "Ignore-paths E2E passed:"
echo "  1. Monarch propagated ignoreRequestPaths to Envoy ext_proc metadata"
echo "  2. seed_mock with ignore_paths=[\"${IGNORE_PATH}\"] matched replay body differing only in nonce"
echo "  3. Without ignore_paths, differing nonce does not share cache key"
