#!/usr/bin/env bash
# Quick verification when the RabbitMQ egress stack is already running.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
# shellcheck source=scripts/lib/e2e-helpers.sh
source "$REPO/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-rmq-egress-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
TRACE_ID="${TRACE_ID:-rmq-egress-verify-$(date +%s)}"
PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ -z "$SHADOW_NS" ]]; then
  log_fail "ShadowTest ${SHADOWTEST} not ready — run ./examples/e2e-rabbitmq-egress-test.sh first"
  exit 1
fi

echo "==> Publish prod message (trace ${TRACE_ID})"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"e2e\":\"rmq-egress-verify\"}' properties='{\"headers\":{\"x-shadow-trace-id\":\"${TRACE_ID}\"}}'
"

sleep 12

for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=50 2>/dev/null | grep -q "rmq egress published.*trace=${TRACE_ID}" \
    || { log_fail "${role} missing egress publish for ${TRACE_ID}"; exit 1; }
  log_success "${role} published RabbitMQ egress"
done

beru_pod=$(kubectl get pods -n beru-system --no-headers | awk '/^beru-/{print $1; exit}')
kubectl logs -n beru-system "$beru_pod" --tail=120 | grep -q "No egress regression for Trace ${TRACE_ID} (rabbitmq)" \
  || { log_fail "Beru missing success log for ${TRACE_ID}"; kubectl logs -n beru-system "$beru_pod" --tail=40 >&2; exit 1; }

log_success "RabbitMQ egress verification passed (trace ${TRACE_ID})"
