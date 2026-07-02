#!/usr/bin/env bash
# E2E: trace-ID-keyed record/replay for HTTP egress — minikube edition.
#
# Validates the core new behaviour: a shadow request with a DIFFERENT body still
# gets the production mock response because lookup is by (trace_id, method, host, path),
# not by body hash.
#
# Prerequisites:
#   eval $(minikube docker-env)         # or use_minikube_docker_env via lib
#   A deployed ShadowTest with spec.recordAndReplay set and status.phase=Ready.
#
# Usage:
#   ./testing/scripts/e2e-python-egress-test.sh
#   SHADOWTEST=my-app-shadow EGRESS_HOST=api.prod.svc ./testing/scripts/e2e-python-egress-test.sh
#   SKIP_DOCKER_ENV=1 ./testing/scripts/e2e-python-egress-test.sh   # if docker-env already applied
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/cluster-minikube.sh
source "$REPO/testing/scripts/lib/cluster-minikube.sh"

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-minikube}"
SKIP_DOCKER_ENV="${SKIP_DOCKER_ENV:-0}"
SHADOWTEST="${SHADOWTEST:-my-app-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
EGRESS_HOST="${EGRESS_HOST:-}"
EGRESS_PATH="${EGRESS_PATH:-/post}"
EGRESS_PROXY="${EGRESS_PROXY:-http://127.0.0.1:15001}"
BERU_LOCAL_PORT="${BERU_LOCAL_PORT:-18080}"   # local port-forward to beru-local
WAIT_PF_SECS="${WAIT_PF_SECS:-10}"

echo "==> Trace-ID egress E2E (minikube profile=${MINIKUBE_PROFILE})"
require_kubectl_cluster

# ---------------------------------------------------------------------------
# Point Docker at minikube's daemon (needed if this script builds/loads images
# later; harmless if docker is not used in the non-build path).
# ---------------------------------------------------------------------------
if [[ "$SKIP_DOCKER_ENV" != "1" ]]; then
  use_minikube_docker_env
fi

# ---------------------------------------------------------------------------
# Resolve ShadowTest state.
# ---------------------------------------------------------------------------
echo "==> Checking ShadowTest ${SHADOWTEST_NS}/${SHADOWTEST}"
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
  -o jsonpath='{.status.phase}' 2>/dev/null || true)
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
  -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$SHADOW_NS" ]]; then
  log_fail "ShadowTest must be Ready (phase=${phase:-<none>} shadowNamespace=${SHADOW_NS:-<none>})"
  exit 1
fi
log_success "ShadowTest Ready — shadowNamespace=${SHADOW_NS}"

if [[ -z "$EGRESS_HOST" ]]; then
  EGRESS_HOST=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.recordAndReplay[0].host}' 2>/dev/null || true)
fi
if [[ -z "$EGRESS_HOST" ]]; then
  log_fail "Set EGRESS_HOST or add spec.recordAndReplay[0].host to ShadowTest"
  exit 1
fi
echo "    Egress target: ${EGRESS_HOST}${EGRESS_PATH}"

kubectl wait -n "$SHADOW_NS" --for=condition=Ready pod \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
  --timeout=120s >/dev/null

POD=$(kubectl get pod -n "$SHADOW_NS" \
  -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=control-a" \
  -o jsonpath='{.items[0].metadata.name}')
if [[ -z "$POD" ]]; then
  log_fail "Could not find control-a shadow pod"
  exit 1
fi
echo "    Shadow pod: ${POD}"

# ---------------------------------------------------------------------------
# Port-forward Beru (prefer beru-local inside shadow namespace).
# ---------------------------------------------------------------------------
BERU_SVC=""
BERU_SVC_NS=""
if kubectl get svc beru-local -n "$SHADOW_NS" >/dev/null 2>&1; then
  BERU_SVC=beru-local
  BERU_SVC_NS="$SHADOW_NS"
elif kubectl get svc beru -n beru-system >/dev/null 2>&1; then
  BERU_SVC=beru
  BERU_SVC_NS=beru-system
else
  log_fail "No beru or beru-local service found"
  exit 1
fi
echo "    Beru service: ${BERU_SVC_NS}/${BERU_SVC} → local :${BERU_LOCAL_PORT}"

kubectl port-forward "svc/${BERU_SVC}" -n "$BERU_SVC_NS" \
  "${BERU_LOCAL_PORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "${PF_PID}" 2>/dev/null || true' EXIT

# Wait for port-forward to be ready.
BERU_URL="http://127.0.0.1:${BERU_LOCAL_PORT}"
for i in $(seq 1 "$WAIT_PF_SECS"); do
  if curl -sf "${BERU_URL}/healthz" >/dev/null 2>&1; then
    break
  fi
  if [[ "$i" -eq "$WAIT_PF_SECS" ]]; then
    log_fail "Beru port-forward did not become ready after ${WAIT_PF_SECS}s"
    exit 1
  fi
  sleep 1
done
log_success "Beru port-forward ready at ${BERU_URL}"

# ---------------------------------------------------------------------------
# Generate a W3C traceparent for this test run.
# ---------------------------------------------------------------------------
TRACE_HEX=$(openssl rand -hex 16)
SPAN_HEX=$(openssl rand -hex 8)
TRACEPARENT="00-${TRACE_HEX}-${SPAN_HEX}-01"
PROD_BODY='{"user_id":42,"query":"SELECT * FROM orders"}'
MOCK_RESP='{"results":[],"trace_replay":true}'
echo "    Trace ID: ${TRACE_HEX}"

# ---------------------------------------------------------------------------
# Helper: curl through Envoy egress proxy from inside the shadow pod.
# ---------------------------------------------------------------------------
shadow_egress_curl() {
  local body="$1"
  local url="http://${EGRESS_HOST}${EGRESS_PATH}"
  local body_q url_q proxy_q tp_q
  body_q=$(printf '%q' "$body")
  url_q=$(printf '%q' "$url")
  proxy_q=$(printf '%q' "$EGRESS_PROXY")
  tp_q=$(printf '%q' "$TRACEPARENT")

  local script
  script="curl -sS -w '__CODE__%{http_code}' \
    -x ${proxy_q} \
    -H 'Content-Type: application/json' \
    -H 'traceparent: ${tp_q}' \
    -d ${body_q} ${url_q}; echo"

  local out
  if kubectl exec -n "$SHADOW_NS" "$POD" -c app -- \
      sh -c 'command -v curl >/dev/null 2>&1' 2>/dev/null; then
    out=$(kubectl exec -n "$SHADOW_NS" "$POD" -c app -- sh -c "$script" 2>/dev/null || true)
  else
    # Ephemeral debug container on the pod's network namespace.
    local dbg="e2e-egress-${RANDOM}"
    # Pull the curl image into minikube before using it.
    minikube -p "$MINIKUBE_PROFILE" image load curlimages/curl:latest 2>/dev/null || true
    out=$(kubectl debug "$POD" -n "$SHADOW_NS" \
      --image=curlimages/curl:latest \
      --target=app \
      --container="$dbg" \
      --attach \
      -- sh -c "$script" 2>&1 || true)
    # Strip kubectl debug noise.
    out=$(echo "$out" | grep -v '^Targeting\|^If you\|^Defaulting' || true)
  fi
  echo "$out"
}

parse_http_code() {
  echo "$1" | grep -oE '__CODE__[0-9]+' | tail -1 | sed 's/__CODE__//'
}

# ---------------------------------------------------------------------------
# Step 1: confirm miss BEFORE seeding (unknown trace → 599).
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 1: egress miss before seeding (expect 599)"
out=$(shadow_egress_curl "$PROD_BODY")
code=$(parse_http_code "$out")
if [[ "$code" != "599" ]]; then
  log_fail "Expected 599 before seed, got ${code:-<empty>} — output: ${out}"
  exit 1
fi
log_success "Got expected 599 before seeding"

# ---------------------------------------------------------------------------
# Step 2: seed Beru via local port-forward (no image pull needed).
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 2: seed Beru /v1/record_egress with trace_id=${TRACE_HEX}"
SEED_JSON=$(printf '{
  "trace_id": "%s",
  "method": "POST",
  "host": "%s",
  "path": "%s",
  "response": {
    "status": 200,
    "headers": {"content-type": "application/json"},
    "body": "%s"
  }
}' "$TRACE_HEX" "$EGRESS_HOST" "$EGRESS_PATH" \
   "$(echo "$MOCK_RESP" | sed 's/"/\\"/g')")

seed_out=$(curl -sf -X POST "${BERU_URL}/v1/record_egress" \
  -H 'Content-Type: application/json' \
  -d "$SEED_JSON" 2>&1 || true)
if ! echo "$seed_out" | grep -q '"hash"'; then
  log_fail "/v1/record_egress did not return hash JSON: ${seed_out}"
  exit 1
fi
log_success "Seeded: ${seed_out}"

# ---------------------------------------------------------------------------
# Step 3: hit with same body (expect 200).
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 3: egress hit — same body (expect 200)"
out=$(shadow_egress_curl "$PROD_BODY")
code=$(parse_http_code "$out")
if [[ "$code" != "200" ]]; then
  log_fail "Expected 200 on same-body hit, got ${code:-<empty>} — output: ${out}"
  exit 1
fi
log_success "Got 200 with same body"

# ---------------------------------------------------------------------------
# Step 4: hit with DIFFERENT body — the core assertion.
# Candidate has changed how it formats the query; body no longer matches
# what was recorded. Old system: 599. New trace-ID system: 200.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 4: egress hit — DIFFERENT body (candidate drift; expect 200)"
CANDIDATE_BODY='{"user_id":42,"query":"SELECT id, status FROM orders","new_field":"v2_added"}'
out=$(shadow_egress_curl "$CANDIDATE_BODY")
code=$(parse_http_code "$out")
if [[ "$code" != "200" ]]; then
  log_fail "Body-divergent candidate should get 200 via trace ID, got ${code:-<empty>}"
  log_fail "This means mock lookup is still body-hash-based, not trace-ID-based."
  exit 1
fi
if ! echo "$out" | grep -q 'trace_replay'; then
  log_fail "Mock response body missing expected field — got: ${out}"
  exit 1
fi
log_success "Got 200 despite different body — trace-ID keyed lookup works"

# ---------------------------------------------------------------------------
# Step 5: Beru must not log Egress Regression for trace-matched requests.
# ---------------------------------------------------------------------------
echo ""
echo "==> Step 5: Beru logs must not contain Egress Regression for trace ${TRACE_HEX}"
beru_logs=""
beru_pod=$(kubectl get pod -n "$BERU_SVC_NS" -l "app=${BERU_SVC}" \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null \
  || kubectl get pod -n "$BERU_SVC_NS" -l app=beru-local \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -n "$beru_pod" ]]; then
  beru_logs=$(kubectl logs -n "$BERU_SVC_NS" "$beru_pod" --tail=200 2>/dev/null || true)
  if echo "$beru_logs" | grep -q "Egress Regression.*${TRACE_HEX}"; then
    log_fail "Beru logs contain unexpected Egress Regression for trace ${TRACE_HEX}"
    echo "$beru_logs" | grep "Egress Regression" | tail -5 | sed 's/^/       /' >&2
    exit 1
  fi
fi
log_success "No Egress Regression logged for trace-matched requests"

# ---------------------------------------------------------------------------
echo ""
log_success "Trace-ID egress E2E passed (trace ${TRACE_HEX}):"
echo "  1. Unseeded trace → 599 (correct miss)"
echo "  2. Seeded via /v1/record_egress with trace_id"
echo "  3. Same body + correct traceparent → 200 (mock hit)"
echo "  4. Different body + correct traceparent → 200 (key: body ignored)"
echo "  5. No spurious Egress Regression in Beru logs"
