#!/usr/bin/env bash
# E2E: Phase 5b RabbitMQ shadow ingress — prod queue, igris-rabbitmq multicast, Beru diff.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-rmq-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
RMQ_WORKER_IMG="${RMQ_WORKER_IMG:-rmq-test-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
RECORDER_IMG="${RECORDER_IMG:-recorder:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
TRACE_HEX="${TRACE_HEX:-$(openssl rand -hex 16)}"
TRACE_ID="${TRACE_ID:-$TRACE_HEX}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"
BERU_HTTP="${BERU_HTTP:-http://beru.beru-system.svc.cluster.local:8080}"

need() { require_cmd "$1"; }

upgrade_crd() {
  local crd="$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl apply -f "$crd"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s
}

echo "==> RabbitMQ E2E (trace ${TRACE_HEX})"

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  (cd "$REPO/testing/example-apps/rmq-test-worker" && make docker-build RMQ_TEST_WORKER_IMG="$RMQ_WORKER_IMG")
  (cd "$REPO/pipeline/igrises/igris-rabbitmq" && make docker-build IGRIS_RABBITMQ_IMG="$IGRIS_RABBITMQ_IMG")
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
  make -C "$REPO/pipeline/recorder" docker-build RECORDER_IMG="$RECORDER_IMG"
fi
if [[ "$SKIP_LOAD" -eq 0 && -n "$KIND_CLUSTER" ]]; then
  kind load docker-image "$RMQ_WORKER_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$IGRIS_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$EGRESS_RELAY_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$RECORDER_IMG" --name "$KIND_CLUSTER"
fi

if [[ "$SKIP_MONARCH_BUILD" -eq 0 ]]; then
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/lib/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/pipeline/monarch"
  else
    make -C "$REPO/pipeline/monarch" docker-build IMG="$MONARCH_IMG"
  fi
  if [[ "$SKIP_LOAD" -eq 0 && -n "$KIND_CLUSTER" ]]; then
    kind load docker-image "$MONARCH_IMG" --name "$KIND_CLUSTER"
  fi
fi

upgrade_crd

if [[ "$SKIP_MONARCH_DEPLOY" -eq 0 ]]; then
  make -C "$REPO/pipeline/monarch" deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev
  if [[ "$SKIP_LOAD" -eq 0 ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-e2e/prod-target.yaml"
kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
kubectl wait --for=condition=Available deployment/rmq-prod-target -n default --timeout=120s

echo "==> Declare prod exchange ${PROD_EXCHANGE} (Monarch also declares on reconcile)"
kubectl exec -n default deploy/rmq-prod-broker -- rabbitmqadmin declare exchange \
  name="${PROD_EXCHANGE}" type=topic durable=true 2>/dev/null || true

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-e2e/shadowtest-rmq.yaml"

echo "==> Wait for ShadowTest Ready"
for i in $(seq 1 60); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
  shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  echo "    phase=$phase queue=${queue:-<none>} msg=${message:-<none>} (${i}/60)"
  if [[ "$phase" == "Ready" && -n "$queue" ]]; then
    log_success "ShadowTest Ready amqpQueueName=$queue"
    break
  fi
  if [[ "$phase" == "Failed" ]]; then
    msg=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
    kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o yaml | tail -30 >&2
    if [[ "$msg" == *'unsupported Igris driver "rabbitmq_message"'* ]]; then
      echo "    Monarch manager is an old binary (CRD updated but controller not rebuilt)." >&2
      echo "    Run:" >&2
      echo "      MONARCH_NO_CACHE=1 make -C pipeline/monarch docker-build IMG=${MONARCH_IMG}" >&2
      echo "      kind load docker-image ${MONARCH_IMG} --name ${KIND_CLUSTER}" >&2
      echo "      kubectl rollout restart deployment/monarch-controller-manager -n monarch-system" >&2
      echo "      kubectl delete shadowtest ${SHADOWTEST} -n ${SHADOWTEST_NS} --wait=true" >&2
      echo "      ./testing/scripts/e2e-rabbitmq-test.sh" >&2
    fi
    log_fail "ShadowTest Failed"
    exit 1
  fi
  sleep 5
done
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
if [[ "$phase" != "Ready" || -z "$queue" ]]; then
  message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
  shadow_ns="${shadow_ns:-$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)}"
  if [[ "$message" == *'waiting for egress-relay-rabbitmq'* && -n "$shadow_ns" ]]; then
    relay_deploy="${SHADOWTEST}-egress-relay-rabbitmq"
    echo "    egress-relay-rabbitmq deployment status:" >&2
    kubectl get deploy "$relay_deploy" -n "$shadow_ns" -o wide 2>/dev/null >&2 || true
    kubectl get pods -n "$shadow_ns" -l "app.kubernetes.io/name=egress-relay-rabbitmq" 2>/dev/null >&2 || true
    echo "    Image must be loaded into Kind: EGRESS_RELAY_RABBITMQ_IMG=${EGRESS_RELAY_RABBITMQ_IMG}" >&2
    echo "    Re-run with SKIP_BUILD=0 or: make -C pipeline/egress-relay-rabbitmq docker-build && kind load docker-image ${EGRESS_RELAY_RABBITMQ_IMG}" >&2
  fi
  log_fail "timed out waiting for Ready (phase=$phase queue=$queue msg=${message:-<none>})"
  exit 1
fi

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
wait_recorder_rollout "$SHADOWTEST" "$SHADOWTEST_NS" "$SHADOW_NS" "$RECORDER_IMG" 120s

echo "==> Verify prod shadow queue exists"
kubectl exec -n default deploy/rmq-prod-broker -- rabbitmqctl list_queues name 2>/dev/null | grep -q "$queue" \
  || { log_fail "prod queue $queue not listed"; exit 1; }
log_success "prod queue $queue present"

echo "==> Declare prod exchange and publish trigger message"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"e2e\":\"rmq\"}' properties='{\"headers\":{\"x-shadow-trace-id\":\"${TRACE_HEX}\"}}'
"

echo "==> Seed Beru egress mock for httpbin GET"
curl_pod_run() {
  kubectl run "rmq-curl-${RANDOM}" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- "$@" 2>&1 | grep -v '^pod "' | grep -v '^If you don' || true
}
curl_pod_run curl -sS -X POST "${BERU_HTTP}/v1/seed_mock" \
  -H 'Content-Type: application/json' \
  -d '{"method":"GET","host":"httpbin.org","path":"/get","body":"","response":{"status":200,"body":"{\"origin\":\"e2e\"}"}}' \
  >/dev/null || true
log_success "seed_mock for httpbin.org GET /get"

sleep 8

echo "==> Verify shadow workers processed messages"
for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  if [[ -z "$pod" ]]; then
    log_fail "no shadow worker pod for role ${role} (expected deploy ${SHADOWTEST}-${role})"
    exit 1
  fi
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=80 2>/dev/null | grep -q "trace=${TRACE_HEX}"; then
    log_fail "no log for trace ${TRACE_HEX} on role ${role} (pod=${pod})"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  log_success "${role} processed trace ${TRACE_HEX}"
done

echo "==> Verify Beru ingress diff"
if ! kubectl logs -n beru-system deploy/beru --tail=150 2>/dev/null | grep -q "No regression for Trace ${TRACE_HEX}"; then
  log_fail "Beru missing 'No regression for Trace ${TRACE_HEX}'"
  kubectl logs -n beru-system deploy/beru --tail=40 >&2 || true
  exit 1
fi
log_success "Beru reported no regression for trace ${TRACE_HEX}"

echo "==> Publish traceparent-only message (W3C ingress parity)"
TRACE_HEX_W3C="${TRACE_HEX_W3C:-$(openssl rand -hex 16)}"
SPAN_HEX="$(openssl rand -hex 8)"
TRACE_TP="00-${TRACE_HEX_W3C}-${SPAN_HEX}-01"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"e2e\":\"rmq-w3c\"}' properties='{\"headers\":{\"traceparent\":\"${TRACE_TP}\"}}'
"
sleep 8

echo "==> Verify shadow workers processed W3C trace ${TRACE_HEX_W3C}"
for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "trace=${TRACE_HEX_W3C}"; then
    log_fail "no log for W3C trace ${TRACE_HEX_W3C} on role ${role}"
    exit 1
  fi
  log_success "${role} processed W3C trace ${TRACE_HEX_W3C}"
done

if ! kubectl logs -n beru-system deploy/beru --tail=200 2>/dev/null | grep -q "No regression for Trace ${TRACE_HEX_W3C}"; then
  log_fail "Beru missing 'No regression for Trace ${TRACE_HEX_W3C}' (traceparent-only)"
  kubectl logs -n beru-system deploy/beru --tail=40 >&2 || true
  exit 1
fi
log_success "Beru reported no regression for W3C trace ${TRACE_HEX_W3C}"

echo ""
log_success "RabbitMQ E2E passed (trace ${TRACE_HEX}, W3C trace ${TRACE_HEX_W3C})"
