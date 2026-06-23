#!/usr/bin/env bash
# E2E: shadow app -> HTTP_PROXY / Envoy :15001 -> Beru ext_proc -> mock or 599
#
# Requires a deployed ShadowTest with spec.recordAndReplay (see e2e-shadowtest.yaml).
# Run after ./testing/scripts/e2e-reset-kind.sh or with an existing Ready stack.
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_DEPLOY="${SHADOW_DEPLOY:-${SHADOWTEST}-control-a}"
EGRESS_HOST="${EGRESS_HOST:-}"
EGRESS_PATH="${EGRESS_PATH:-/post}"
EGRESS_PROXY="${EGRESS_PROXY:-http://127.0.0.1:15001}"
BERU_HTTP="${BERU_HTTP:-http://beru.beru-system.svc.cluster.local:8080}"
MISS_BODY="${MISS_BODY:-{\"e2e_egress_miss\":$(date +%s)}}"
HIT_BODY="${HIT_BODY:-{\"foo\":1}}"

EGRESS_LAST_CODE=""
EGRESS_LAST_BODY=""

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing command: $1" >&2; exit 1; }
}
need kubectl

parse_egress_output() {
  local out="$1"
  out=$(echo "$out" | grep -v '^Targeting container' | grep -v "^If you don't see" | grep -v '^Defaulting container')
  local line
  # Prefer a line ending with a real HTTP status (not curl's 000 while retrying).
  line=$(echo "$out" | grep -E '__CODE__[1-9][0-9]{2}$' | tail -1 || true)
  if [[ -z "$line" ]]; then
    line=$(echo "$out" | grep -E '__CODE__[0-9]+$' | tail -1 || true)
  fi
  if [[ -z "$line" ]]; then
    echo "ERROR: could not parse egress curl output:${out}" >&2
    exit 1
  fi
  EGRESS_LAST_CODE=$(echo "$line" | sed -E 's/.*__CODE__([0-9]+)$/\1/')
  EGRESS_LAST_BODY=$(echo "$line" | sed -E 's/__CODE__[0-9]+$//')
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

echo "==> Egress E2E prerequisites"
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  echo "ERROR: ShadowTest must be Ready (phase=$phase shadowNamespace=$SHADOW_NS)" >&2
  exit 1
fi

if [[ -z "$EGRESS_HOST" ]]; then
  EGRESS_HOST=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.recordAndReplay[0].host}' 2>/dev/null || true)
fi
if [[ -z "$EGRESS_HOST" ]]; then
  echo "ERROR: set EGRESS_HOST or add spec.recordAndReplay[0].host to ShadowTest" >&2
  exit 1
fi
echo "    shadowNamespace=$SHADOW_NS recordAndReplayHost=$EGRESS_HOST deploy=$SHADOW_DEPLOY"

kubectl wait -n "$SHADOW_NS" --for=condition=Ready pod \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" --timeout=120s
kubectl wait -n beru-system --for=condition=Ready pod \
  -l app.kubernetes.io/name=beru --timeout=120s

echo "==> Verify app HTTP_PROXY env"
http_proxy=$(kubectl get deploy "$SHADOW_DEPLOY" -n "$SHADOW_NS" \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="app")].env[?(@.name=="HTTP_PROXY")].value}')
if [[ "$http_proxy" != "$EGRESS_PROXY" ]]; then
  echo "ERROR: expected app HTTP_PROXY=$EGRESS_PROXY, got ${http_proxy:-<unset>}" >&2
  exit 1
fi
echo "    HTTP_PROXY=$http_proxy"

echo "==> Verify Envoy egress_proxy config"
envoy_yaml=$(kubectl get cm -n "$SHADOW_NS" "${SHADOW_DEPLOY}-envoy" -o jsonpath='{.data.envoy\.yaml}')
for needle in egress_proxy "x-shadow-mode" "value: \"egress\"" request_body_mode: BUFFERED "$EGRESS_HOST" "${EGRESS_HOST}:*"; do
  if ! grep -qF "$needle" <<<"$envoy_yaml"; then
    echo "ERROR: Envoy config missing expected egress fragment: $needle" >&2
    exit 1
  fi
done
echo "    egress_proxy + ext_proc + domains OK"

POD=$(kubectl get pod -n "$SHADOW_NS" \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
  -o jsonpath='{.items[0].metadata.name}')
if [[ -z "$POD" ]]; then
  echo "ERROR: could not find shadow pod for $SHADOW_DEPLOY" >&2
  exit 1
fi

echo "==> Egress miss (expect HTTP 599 before seeding mock)"
egress_curl "$MISS_BODY"
miss_code=$EGRESS_LAST_CODE
miss_body=$EGRESS_LAST_BODY
echo "    status=$miss_code body=$miss_body"
if [[ "$miss_code" != "599" ]]; then
  echo "ERROR: expected HTTP 599 on unseeded egress, got $miss_code" >&2
  exit 1
fi
if [[ "$miss_body" != *"Egress Regression"* ]]; then
  echo "ERROR: expected body to contain 'Egress Regression', got: $miss_body" >&2
  exit 1
fi

echo "==> Beru log contains Egress Regression for miss"
if ! kubectl logs -n beru-system deploy/beru --tail=80 | grep -q 'Egress Regression'; then
  echo "ERROR: Beru logs missing 'Egress Regression' after miss request" >&2
  kubectl logs -n beru-system deploy/beru --tail=30 >&2 || true
  exit 1
fi

SEED_JSON=$(cat <<EOF
{
  "method": "POST",
  "host": "${EGRESS_HOST}",
  "path": "${EGRESS_PATH}",
  "body": ${HIT_BODY},
  "ignore_paths": [],
  "response": {
    "status": 200,
    "headers": {"content-type": "application/json"},
    "body": "{\"mock\":true}"
  }
}
EOF
)

echo "==> Seed mock via POST ${BERU_HTTP}/v1/seed_mock"
seed_out=$(kubectl run "e2e-egress-seed-$(date +%s)" --rm -i --restart=Never \
  --image=curlimages/curl:latest -- \
  curl -sf -X POST "${BERU_HTTP}/v1/seed_mock" \
  -H 'Content-Type: application/json' \
  -d "$SEED_JSON")
echo "    $seed_out"
if ! grep -q '"hash"' <<<"$seed_out"; then
  echo "ERROR: seed_mock did not return hash JSON: $seed_out" >&2
  exit 1
fi

echo "==> Egress hit (expect HTTP 200 + mock body)"
egress_curl "$HIT_BODY"
hit_code=$EGRESS_LAST_CODE
hit_body=$EGRESS_LAST_BODY
echo "    status=$hit_code body=$hit_body"
if [[ "$hit_code" != "200" ]]; then
  echo "ERROR: expected HTTP 200 on seeded egress, got $hit_code" >&2
  exit 1
fi
if [[ "$hit_body" != *'"mock":true'* && "$hit_body" != *'"mock": true'* ]]; then
  echo "ERROR: expected mock body, got: $hit_body" >&2
  exit 1
fi

echo ""
echo "Egress E2E passed:"
echo "  1. HTTP_PROXY -> Envoy :15001 configured on shadow app"
echo "  2. Unseeded POST ${EGRESS_HOST}${EGRESS_PATH} -> 599 Egress Regression"
echo "  3. seed_mock + matching request -> 200 {\"mock\":true}"
