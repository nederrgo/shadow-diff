#!/usr/bin/env bash
# E2E: RabbitMQ egress diffing — Firehose trace → egress-relay-rabbitmq → Beru diff-of-diffs.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-rmq-egress-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
RMQ_WORKER_IMG="${RMQ_WORKER_IMG:-rmq-test-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
TRACE_ID="${TRACE_ID:-rmq-egress-e2e-$(date +%s)}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"

need() { require_cmd "$1"; }

strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container' | grep -v 'credentials and sensitive'
}

in_cluster_curl() {
  local name="$1"
  shift
  local out
  out=$(kubectl run "$name" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- "$@" 2>&1) || true
  strip_kubectl_run_output "$out"
}

upgrade_crd() {
  kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true
}

trap '[[ $? -ne 0 ]] && log_fail "RabbitMQ egress E2E failed (see above)"' EXIT

echo "==> RabbitMQ egress E2E (trace ${TRACE_ID})"
require_kubectl_cluster
if [[ "$SKIP_BUILD" != "1" || "$SKIP_LOAD" != "1" ]]; then
  require_docker
fi

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build rmq-test-worker image"
  make -C "$REPO/testing/example-apps/rmq-test-worker" docker-build RMQ_TEST_WORKER_IMG="$RMQ_WORKER_IMG"
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
  need kind
  [[ -n "${KIND_CLUSTER}" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
  kind load docker-image "$RMQ_WORKER_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$IGRIS_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$EGRESS_RELAY_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  docker pull rabbitmq:3-management-alpine 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull rabbitmq:3-management-alpine 2>/dev/null || true
  kind load docker-image rabbitmq:3-management-alpine --name "$KIND_CLUSTER" 2>/dev/null || true
fi

kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed — run: ./testing/scripts/e2e-reset-kind.sh"
  exit 1
}

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
kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-egress-e2e/prod-target-egress.yaml"

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-egress-e2e/shadowtest-rmq-egress.yaml"

kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
kubectl wait --for=condition=Available deployment/rmq-prod-target -n default --timeout=120s

echo "==> Wait for ShadowTest Ready"
SHADOW_NS=""
for i in $(seq 1 60); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  relay_ok=0
  if [[ -n "$SHADOW_NS" ]] && kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$SHADOW_NS" >/dev/null 2>&1; then
    avail=$(kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$SHADOW_NS" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
  fi
  echo "    phase=${phase:-<none>} queue=${queue:-<none>} shadowNS=${SHADOW_NS:-<pending>} egress-relay-ready=${relay_ok} (${i}/60)"
  if [[ "$phase" == "Ready" && -n "$queue" && "$relay_ok" == "1" ]]; then
    break
  fi
  if [[ "$phase" == "Failed" ]]; then
    kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o yaml | tail -30 >&2
    log_fail "ShadowTest Failed"
    exit 1
  fi
  sleep 5
done

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
if [[ -z "$SHADOW_NS" ]]; then
  log_fail "no shadowNamespace"
  exit 1
fi
log_success "ShadowTest Ready namespace=${SHADOW_NS}"

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

echo "==> Verify egress-relay-rabbitmq deployment"
relay_deploy="${SHADOWTEST}-egress-relay-rabbitmq"
kubectl rollout status "deployment/${relay_deploy}" -n "$SHADOW_NS" --timeout=180s
beru_url=$(kubectl get deploy "$relay_deploy" -n "$SHADOW_NS" \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="BERU_HTTP_URL")].value}')
if [[ "$beru_url" != "http://beru.beru-system.svc.cluster.local:8080" ]]; then
  log_fail "egress-relay BERU_HTTP_URL=${beru_url:-<unset>}"
  exit 1
fi
log_success "egress-relay-rabbitmq ready BERU_HTTP_URL=${beru_url}"

for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

echo "==> Declare prod exchange and publish ingress trigger"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"e2e\":\"rmq-egress\"}' properties='{\"headers\":{\"x-shadow-trace-id\":\"${TRACE_ID}\"}}'
"
log_success "published prod message trace=${TRACE_ID}"

echo "==> Wait for shadow workers, egress-relay, and Beru diff"
sleep 15

for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  if [[ -z "$pod" ]]; then
    log_fail "no shadow worker pod for role ${role}"
    exit 1
  fi
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=100 2>/dev/null | grep -q "trace=${TRACE_ID}"; then
    log_fail "${role} app logs missing trace=${TRACE_ID}"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=100 2>/dev/null | grep -q "rmq egress published"; then
    log_fail "${role} app logs missing rmq egress publish"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  log_success "${role} consumed and published RabbitMQ egress trace=${TRACE_ID}"
done

relay_pod=$(kubectl get pods -n "$SHADOW_NS" -l "app.kubernetes.io/name=egress-relay-rabbitmq" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -n "$relay_pod" ]]; then
  kubectl logs -n "$SHADOW_NS" "$relay_pod" --tail=40 2>/dev/null | head -5 || true
fi

echo "==> Verify Beru RabbitMQ egress diff"
beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
if [[ -z "$beru_pod" ]]; then
  log_fail "Beru pod not found in beru-system"
  exit 1
fi
if ! kubectl logs -n beru-system "$beru_pod" --tail=200 | grep -q "No egress regression for Trace ${TRACE_ID} (rabbitmq)"; then
  log_fail "Beru logs missing 'No egress regression for Trace ${TRACE_ID} (rabbitmq)'"
  kubectl logs -n beru-system "$beru_pod" --tail=80 >&2 || true
  exit 1
fi
log_success "Beru reported no RabbitMQ egress regression for trace ${TRACE_ID}"

echo "==> W3C traceparent-only RabbitMQ egress (no x-shadow-trace-id on egress publish)"
TRACE_HEX="${TRACE_HEX:-$(openssl rand -hex 16)}"
SPAN_HEX="$(openssl rand -hex 8)"
TRACE_TP="00-${TRACE_HEX}-${SPAN_HEX}-01"

kubectl set env deployment/rmq-prod-target -n default RMQ_EGRESS_TRACEPARENT_ONLY=1 >/dev/null 2>&1 || true
for role in control-a control-b candidate; do
  kubectl set env "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" RMQ_EGRESS_TRACEPARENT_ONLY=1
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"e2e\":\"rmq-egress-w3c\"}' properties='{\"headers\":{\"traceparent\":\"${TRACE_TP}\"}}'
"
log_success "published traceparent-only prod message trace_hex=${TRACE_HEX}"
sleep 15

for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  if [[ -z "$pod" ]]; then
    log_fail "no shadow worker pod for role ${role}"
    exit 1
  fi
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "trace=${TRACE_HEX}"; then
    log_fail "${role} app logs missing trace=${TRACE_HEX} (W3C consume)"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "traceparent_only=true"; then
    log_fail "${role} app logs missing traceparent_only egress publish"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  log_success "${role} consumed W3C trace and published traceparent-only egress trace=${TRACE_HEX}"
done

if ! kubectl logs -n beru-system "$beru_pod" --tail=300 | grep -q "No egress regression for Trace ${TRACE_HEX} (rabbitmq)"; then
  log_fail "Beru logs missing 'No egress regression for Trace ${TRACE_HEX} (rabbitmq)' (traceparent-only egress)"
  kubectl logs -n beru-system "$beru_pod" --tail=80 >&2 || true
  exit 1
fi
log_success "Beru reported no RabbitMQ egress regression for W3C trace ${TRACE_HEX}"

trap - EXIT
log_success "RabbitMQ egress E2E passed (trace ${TRACE_ID}, W3C trace ${TRACE_HEX})"
