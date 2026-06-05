#!/usr/bin/env bash
# Quick check: multicast a write via Igris and confirm Beru reports no mongo egress regression.
#
# Prerequisites (one-time setup):
#   - Kind cluster with Beru + Monarch + mongo-test ShadowTest deployed
#   - ./testing/scripts/e2e-reset-kind.sh  OR  ./testing/scripts/e2e-mongo-egress-test.sh
#
# Usage:
#   ./testing/scripts/verify-mongo-egress.sh
#   TRACE_ID=my-test-123 ./testing/scripts/verify-mongo-egress.sh
#   SHADOWTEST_NS=default WAIT_SECS=45 ./testing/scripts/verify-mongo-egress.sh
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-mongo-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
BERU_NS="${BERU_NS:-beru-system}"
TRACE_ID="${TRACE_ID:-mongo-verify-$(date +%s)}"
WAIT_SECS="${WAIT_SECS:-40}"

strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container' | grep -v 'credentials and sensitive'
}

require_cmd kubectl

trap '[[ $? -ne 0 ]] && log_fail "mongo egress verification failed (trace '"${TRACE_ID}"')"' EXIT

echo "==> Mongo egress verification"
echo "    trace=${TRACE_ID}"

kubectl get deploy -n "$BERU_NS" beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed in ${BERU_NS} — run ./testing/scripts/e2e-reset-kind.sh first"
  exit 1
}

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ -z "$SHADOW_NS" ]]; then
  log_fail "ShadowTest ${SHADOWTEST_NS}/${SHADOWTEST} has no shadowNamespace — is it Ready?"
  kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" 2>/dev/null || true
  exit 1
fi
log_success "shadow namespace=${SHADOW_NS}"

echo "==> Check shadow pods (expect 2/2 for control-a, control-b, candidate)"
for role in control-a control-b candidate; do
  dep="${SHADOWTEST}-${role}"
  ready=$(kubectl get deploy "$dep" -n "$SHADOW_NS" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
  if [[ "${ready:-0}" -lt 1 ]]; then
    log_fail "${dep} not ready (${ready:-0} replicas)"
    kubectl get pods -n "$SHADOW_NS" -l "app.kubernetes.io/name=${dep}" 2>/dev/null || kubectl get pods -n "$SHADOW_NS" | grep "$dep" || true
    exit 1
  fi
  pod=$(kubectl get pods -n "$SHADOW_NS" --no-headers 2>/dev/null | awk -v d="$dep" '$1 ~ d {print $1; exit}')
  containers=$(kubectl get pod "$pod" -n "$SHADOW_NS" -o jsonpath='{.status.containerStatuses[*].ready}' 2>/dev/null || true)
  if [[ "$containers" != *"true true"* ]]; then
    log_fail "${dep} pod ${pod} not 2/2 ready: ${containers:-<unknown>}"
    kubectl describe pod "$pod" -n "$SHADOW_NS" 2>&1 | tail -20
    exit 1
  fi
  log_success "${dep} pod ${pod} is 2/2"
done

IGRIS_URL="http://${SHADOWTEST}-igris.${SHADOW_NS}.svc.cluster.local:8888"
echo "==> Multicast POST ${IGRIS_URL}/write"
write_out=$(kubectl run "mongo-verify-${RANDOM}" --rm -i --restart=Never -n default \
  --image=curlimages/curl:latest -- \
  curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
  -X POST "${IGRIS_URL}/write" \
  -H "Content-Type: application/json" \
  -H "x-shadow-trace-id: ${TRACE_ID}" \
  -d '{"data":"verify-mongo-egress"}' 2>&1) || true
write_out=$(strip_kubectl_run_output "$write_out")
echo "    curl: ${write_out}"
if [[ "$write_out" != *'__HTTP_CODE__202'* ]]; then
  log_fail "expected HTTP 202 from Igris, got: ${write_out:-<empty>}"
  exit 1
fi
log_success "Igris accepted multicast (HTTP 202)"

echo "==> Wait up to ${WAIT_SECS}s for Beru mongo egress diff (trace ${TRACE_ID})"
beru_pod=$(kubectl get pods -n "$BERU_NS" -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$beru_pod" ]]; then
  beru_pod=$(kubectl get pods -n "$BERU_NS" --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
fi
if [[ -z "$beru_pod" ]]; then
  log_fail "could not find Beru pod in ${BERU_NS}"
  exit 1
fi

success_msg="No egress regression for Trace ${TRACE_ID} (mongodb)"
timeout_msg="Timed out waiting for Trace ${TRACE_ID} (mongodb egress)"

for i in $(seq 1 "$WAIT_SECS"); do
  logs=$(kubectl logs -n "$BERU_NS" "$beru_pod" --tail=200 2>/dev/null || true)
  if grep -qF "$success_msg" <<<"$logs"; then
    log_success "$success_msg"
    trap - EXIT
    echo ""
    echo "Mongo egress verification passed."
    echo "  trace:  ${TRACE_ID}"
    echo "  shadow: ${SHADOW_NS}"
    exit 0
  fi
  if grep -qF "$timeout_msg" <<<"$logs"; then
    log_fail "Beru timed out waiting for mongo ALS entries"
    echo "--- recent Beru logs ---"
    kubectl logs -n "$BERU_NS" "$beru_pod" --tail=40 2>&1 | grep -E "${TRACE_ID}|mongodb|Skipping" || kubectl logs -n "$BERU_NS" "$beru_pod" --tail=20
    exit 1
  fi
  sleep 1
done

log_fail "timed out after ${WAIT_SECS}s — Beru log missing: ${success_msg}"
echo "--- recent Beru logs ---"
kubectl logs -n "$BERU_NS" "$beru_pod" --tail=40 2>&1 | grep -E "${TRACE_ID}|mongodb|Skipping" || kubectl logs -n "$BERU_NS" "$beru_pod" --tail=20
echo "--- shadow app logs (control-a) ---"
kubectl logs -n "$SHADOW_NS" "deploy/${SHADOWTEST}-control-a" -c app --tail=10 2>/dev/null || true
exit 1
