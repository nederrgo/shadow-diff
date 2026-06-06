#!/usr/bin/env bash
# E2E: OTel auto-instrumentation propagates W3C traceparent across RabbitMQ consume/publish.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/otel-bootstrap.sh
source "$REPO/testing/scripts/lib/otel-bootstrap.sh"

SHADOWTEST="${SHADOWTEST:-otel-rmq-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_NS="${SHADOW_NS:-shadow-default-otel-rmq-test-shadow}"
NODEJS_TEST_WORKER_IMG="${NODEJS_TEST_WORKER_IMG:-nodejs-test-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"
SKIP_OTEL_BOOTSTRAP="${SKIP_OTEL_BOOTSTRAP:-0}"

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"

PORT_FWD_PIDS=()

upgrade_crd() {
  kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true
}

cleanup() {
  echo "==> Cleaning up OTel E2E resources..."
  kubectl delete shadowtest "${SHADOWTEST}" -n "${SHADOWTEST_NS}" --ignore-not-found --wait=false
  kubectl delete namespace "${SHADOW_NS}" --ignore-not-found=true
  for pid in "${PORT_FWD_PIDS[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
  jobs -p | xargs kill -9 2>/dev/null || true
}
trap cleanup EXIT

echo "==> OTel RabbitMQ egress E2E"
require_kubectl_cluster
if [[ "$SKIP_BUILD" != "1" || "$SKIP_LOAD" != "1" ]]; then
  require_docker
fi

if [[ "$SKIP_OTEL_BOOTSTRAP" != "1" ]]; then
  if ! otel_operator_ready 2>/dev/null; then
    echo "==> OpenTelemetry Operator not ready — running otel-bootstrap"
    install_otel_stack
  else
    echo "==> OpenTelemetry Operator already installed"
  fi
fi

kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed — run: ./testing/scripts/e2e-reset-kind.sh"
  exit 1
}

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build nodejs-test-worker image"
  make -C "$REPO/testing/example-apps/nodejs-test-worker" docker-build NODEJS_TEST_WORKER_IMG="$NODEJS_TEST_WORKER_IMG"
  echo "==> Build igris-rabbitmq image"
  make -C "$REPO/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="$IGRIS_RABBITMQ_IMG"
  echo "==> Build egress-relay-rabbitmq image"
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  echo "==> Build Beru image"
  make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  require_cmd kind
  [[ -n "${KIND_CLUSTER}" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
  kind load docker-image "$NODEJS_TEST_WORKER_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$IGRIS_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$EGRESS_RELAY_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  docker pull rabbitmq:3-management-alpine 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull rabbitmq:3-management-alpine 2>/dev/null || true
  kind load docker-image rabbitmq:3-management-alpine --name "$KIND_CLUSTER" 2>/dev/null || true
fi

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/lib/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/pipeline/monarch"
  else
    make -C "$REPO/pipeline/monarch" docker-build IMG="$MONARCH_IMG"
  fi
fi

if [[ "$SKIP_LOAD" != "1" && "$SKIP_MONARCH_BUILD" != "1" ]]; then
  kind load docker-image "$MONARCH_IMG" --name "$KIND_CLUSTER"
fi

if [[ "$SKIP_MONARCH_DEPLOY" != "1" ]]; then
  make -C "$REPO/pipeline/monarch" deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev
  if [[ "$SKIP_LOAD" != "1" ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

kubectl set image deployment/beru -n beru-system beru="$BERU_IMG" --record=false 2>/dev/null || true
kubectl rollout status deployment/beru -n beru-system --timeout=120s 2>/dev/null || true

upgrade_crd

kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-otel-e2e/prod-target-nodejs.yaml"

echo "==> Pre-provision Instrumentation CR in ${SHADOW_NS} (before ShadowTest creates pods)"
bash "$REPO/testing/scripts/lib/apply-otel-instrumentation.sh" "$SHADOW_NS"

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-otel-e2e/shadowtest-otel-rmq.yaml"

kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
kubectl wait --for=condition=Available deployment/rmq-prod-target -n default --timeout=120s

echo "==> Wait for ShadowTest Ready"
for i in $(seq 1 60); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  actual_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  relay_ok=0
  if [[ -n "$actual_ns" ]] && kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$actual_ns" >/dev/null 2>&1; then
    avail=$(kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$actual_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
  fi
  echo "    phase=${phase:-<none>} queue=${queue:-<none>} shadowNS=${actual_ns:-<pending>} egress-relay-ready=${relay_ok} (${i}/60)"
  if [[ "$phase" == "Ready" && -n "$queue" && "$relay_ok" == "1" ]]; then
    SHADOW_NS="$actual_ns"
    break
  fi
  if [[ "$phase" == "Failed" ]]; then
    kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o yaml | tail -30 >&2
    log_fail "ShadowTest Failed"
    exit 1
  fi
  sleep 5
done

if [[ -z "$SHADOW_NS" ]]; then
  log_fail "no shadowNamespace"
  exit 1
fi
log_success "ShadowTest Ready namespace=${SHADOW_NS}"

echo "==> Assert OTel injection on shadow worker pods"
chmod +x "$REPO/testing/scripts/assert-otel-injected.sh"
for role in control-a control-b candidate; do
  "$REPO/testing/scripts/assert-otel-injected.sh" "$SHADOW_NS" "$role" "$SHADOWTEST"
done

echo "==> Verify RabbitMQ broker Firehose configuration"
for role in control-a control-b candidate; do
  dep="rabbitmq-${role}"
  plugins_env=$(kubectl get deploy "$dep" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="dependency")].env[?(@.name=="RABBITMQ_ENABLED_PLUGINS_FILE")].value}')
  if [[ "$plugins_env" != "/custom-config/enabled_plugins" ]]; then
    log_fail "${dep}: expected RABBITMQ_ENABLED_PLUGINS_FILE=/custom-config/enabled_plugins, got ${plugins_env:-<unset>}"
    exit 1
  fi
  probe=$(kubectl get deploy "$dep" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="dependency")].startupProbe.exec.command[*]}')
  if [[ "$probe" != *"trace_on"* || "$probe" != *"firehose_ready"* ]]; then
    log_fail "${dep}: startup probe missing trace_on / firehose_ready check"
    exit 1
  fi
  log_success "${dep} Firehose startup probe + TCP readiness configured"
done

relay_deploy="${SHADOWTEST}-egress-relay-rabbitmq"
kubectl rollout status "deployment/${relay_deploy}" -n "$SHADOW_NS" --timeout=180s
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

TRACE_HEX="$(openssl rand -hex 16)"
SPAN_HEX="$(openssl rand -hex 8)"
TRACE_TP="00-${TRACE_HEX}-${SPAN_HEX}-01"

PROD_QUEUE=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}')
if [[ -z "$PROD_QUEUE" ]]; then
  log_fail "ShadowTest status.amqpQueueName is empty"
  exit 1
fi

echo "==> Verify prod shadow queue ${PROD_QUEUE} exists"
if ! kubectl exec -n default deploy/rmq-prod-broker -- rabbitmqctl list_queues name 2>/dev/null | grep -q "$PROD_QUEUE"; then
  log_fail "prod queue ${PROD_QUEUE} not listed on rmq-prod-broker"
  exit 1
fi
log_success "prod queue ${PROD_QUEUE} present"

kubectl rollout status "deployment/${SHADOWTEST}-igris-rabbitmq" -n "$SHADOW_NS" --timeout=120s

echo "==> Publish traceparent-only prod message (trace ${TRACE_HEX})"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"e2e\":\"otel-rmq\"}' properties='{\"headers\":{\"traceparent\":\"${TRACE_TP}\"}}'
"
log_success "published traceparent-only prod message trace_hex=${TRACE_HEX}"

echo "==> Wait for shadow workers to consume message"
wait_for_worker_consume() {
  local role="$1"
  local pod
  for i in $(seq 1 30); do
    pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
    if [[ -n "$pod" ]] && kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=100 2>/dev/null | grep -q "consumed routing_key="; then
      return 0
    fi
    sleep 2
  done
  return 1
}

for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  if [[ -z "$pod" ]]; then
    log_fail "no shadow worker pod for role ${role}"
    exit 1
  fi
  if ! wait_for_worker_consume "$role"; then
    log_fail "${role} app logs missing consumed message after 60s"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    echo "    igris-rabbitmq logs:" >&2
    kubectl logs -n "$SHADOW_NS" "deploy/${SHADOWTEST}-igris-rabbitmq" --tail=30 >&2 || true
    exit 1
  fi
  if kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=100 2>/dev/null | grep -q "${TRACE_HEX}"; then
    log_fail "${role} app logs contain trace hex ${TRACE_HEX} (worker must be trace-unaware)"
    exit 1
  fi
  log_success "${role} consumed message without logging trace id (trace-unaware app)"
done

echo "==> Wait for egress-relay and Beru diff"
sleep 15

echo "==> Verify Beru RabbitMQ egress diff"
beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
if [[ -z "$beru_pod" ]]; then
  log_fail "Beru pod not found in beru-system"
  exit 1
fi
if ! kubectl logs -n beru-system "$beru_pod" --tail=200 | grep -q "No egress regression for Trace ${TRACE_HEX} (rabbitmq)"; then
  log_fail "Beru logs missing 'No egress regression for Trace ${TRACE_HEX} (rabbitmq)'"
  kubectl logs -n beru-system "$beru_pod" --tail=80 >&2 || true
  exit 1
fi
log_success "Beru reported no RabbitMQ egress regression for trace ${TRACE_HEX}"

trap - EXIT
cleanup
log_success "OTel RabbitMQ egress E2E passed (W3C trace ${TRACE_HEX})"
