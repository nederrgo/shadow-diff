#!/usr/bin/env bash
# E2E: Ultimate hybrid — RabbitMQ ingress + Mongo OTLP + HTTP record/replay + RMQ Firehose egress.
# Candidate executes extra Mongo write + extra RMQ publish; Beru flags dual count regressions.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/otel-bootstrap.sh
source "$REPO/testing/scripts/lib/otel-bootstrap.sh"
# shellcheck source=testing/scripts/lib/siphon-config.sh
source "$REPO/testing/scripts/lib/siphon-config.sh"

SHADOWTEST="${SHADOWTEST:-python-hybrid-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_NS="${SHADOW_NS:-shadow-default-python-hybrid-shadow}"
PYTHON_TEST_WORKER_IMG="${PYTHON_TEST_WORKER_IMG:-python-test-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
MONGO_IMAGE="${MONGO_IMAGE:-mongo:4.4}"
WAIT_SECS="${WAIT_SECS:-45}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"
SKIP_OTEL_BOOTSTRAP="${SKIP_OTEL_BOOTSTRAP:-0}"

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"
MANIFEST_DIR="$REPO/testing/scripts/manifests/rabbitmq-otel-e2e"

upgrade_crd() {
  kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true
}

trap '[[ $? -ne 0 ]] && log_fail "python hybrid E2E failed (see above)"' EXIT

echo "==> Python hybrid E2E (Mongo OTLP + HTTP replay + RMQ Firehose)"
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
  echo "==> Build python-test-worker image"
  make -C "$REPO/testing/example-apps/python-test-worker" docker-build PYTHON_TEST_WORKER_IMG="$PYTHON_TEST_WORKER_IMG"
  echo "==> Build igris-rabbitmq image"
  make -C "$REPO/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="$IGRIS_RABBITMQ_IMG"
  echo "==> Build egress-relay-rabbitmq image"
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  echo "==> Build Beru image"
  if [[ "${BERU_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/lib/docker.sh" build --no-cache -t "$BERU_IMG" "$REPO/pipeline/beru"
  else
    make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
      bash "$REPO/testing/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
  fi
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  require_cmd kind
  [[ -n "${KIND_CLUSTER}" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
  kind load docker-image "$PYTHON_TEST_WORKER_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$IGRIS_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$EGRESS_RELAY_RABBITMQ_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  docker pull "$MONGO_IMAGE" 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull "$MONGO_IMAGE" 2>/dev/null || true
  kind load docker-image "$MONGO_IMAGE" --name "$KIND_CLUSTER" 2>/dev/null || true
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

kubectl apply -f "$REPO/pipeline/beru/deploy/deployment.yaml"
kubectl set image deployment/beru -n beru-system beru="$BERU_IMG" --record=false 2>/dev/null || true
if [[ "$SKIP_LOAD" != "1" ]]; then
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER" 2>/dev/null || true
fi
kubectl rollout restart deployment/beru -n beru-system 2>/dev/null || true
kubectl rollout status deployment/beru -n beru-system --timeout=120s 2>/dev/null || true

upgrade_crd

echo "==> Deploy prod stack"
kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
kubectl apply -f "$MANIFEST_DIR/prod-mongo.yaml"
# Headless clusterIP cannot be patched from an assigned ClusterIP; recreate Service only.
if kubectl get svc user-service -n prod -o jsonpath='{.spec.clusterIP}' 2>/dev/null | grep -qv '^None$'; then
  echo "==> Recreate user-service as headless (delete ClusterIP Service, keep Deployment)"
  kubectl delete svc user-service -n prod --ignore-not-found --wait=true
fi
kubectl apply -f "$MANIFEST_DIR/prod-user-service.yaml"
kubectl apply -f "$MANIFEST_DIR/prod-python-worker.yaml"

kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
kubectl wait --for=condition=Available deployment/mongo-prod -n default --timeout=180s
kubectl wait --for=condition=Available deployment/user-service -n prod --timeout=180s
kubectl wait --for=condition=Available deployment/python-prod-worker -n default --timeout=180s

echo "==> Pre-provision Instrumentation CR in ${SHADOW_NS}"
bash "$REPO/testing/scripts/lib/apply-otel-instrumentation.sh" "$SHADOW_NS"

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$MANIFEST_DIR/shadowtest-python-hybrid.yaml"

echo "==> Wait for ShadowTest Ready"
SHADOW_NS=""
for i in $(seq 1 60); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  actual_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  siphon=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}' 2>/dev/null || true)
  relay_ok=0
  mongo_ok=0
  if [[ -n "$actual_ns" ]] && kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$actual_ns" >/dev/null 2>&1; then
    avail=$(kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$actual_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
  fi
  if [[ -n "$actual_ns" ]] && kubectl get deploy mongodb-control-a -n "$actual_ns" >/dev/null 2>&1; then
    avail=$(kubectl get deploy mongodb-control-a -n "$actual_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && mongo_ok=1
  fi
  echo "    phase=${phase:-<none>} queue=${queue:-<none>} siphon=${siphon:-<none>} shadowNS=${actual_ns:-<pending>} relay=${relay_ok} mongo=${mongo_ok} (${i}/60)"
  if [[ "$phase" == "Ready" && -n "$queue" && "$relay_ok" == "1" && "$mongo_ok" == "1" && "$siphon" == "Ready" ]]; then
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

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
if [[ -z "$SHADOW_NS" ]]; then
  log_fail "no shadowNamespace"
  exit 1
fi
log_success "ShadowTest Ready namespace=${SHADOW_NS} siphonPhase=Ready"

echo "==> Apply Instrumentation CR in ${SHADOW_NS} (post-create)"
bash "$REPO/testing/scripts/lib/apply-otel-instrumentation.sh" "$SHADOW_NS"

echo "==> Restart shadow apps for OTel webhook injection"
for role in control-a control-b candidate; do
  kubectl rollout restart "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS"
done
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

chmod +x "$REPO/testing/scripts/assert-otel-injected.sh"
for role in control-a control-b candidate; do
  "$REPO/testing/scripts/assert-otel-injected.sh" "$SHADOW_NS" "$role" "$SHADOWTEST"
done

echo "==> Verify Siphon recorder config for HTTP recordAndReplay"
wait_siphon_configured 1

relay_deploy="${SHADOWTEST}-egress-relay-rabbitmq"
kubectl rollout status "deployment/${relay_deploy}" -n "$SHADOW_NS" --timeout=180s
kubectl rollout status "deployment/${SHADOWTEST}-igris-rabbitmq" -n "$SHADOW_NS" --timeout=120s

# Gate 2: arm Siphon immediately before prod traffic (see testing/scripts/lib/siphon-config.sh).
echo "==> Arm Siphon before prod traffic (config + BPF)"
nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"
wait_siphon_configured 1
refresh_netobserv_hooks "default" "app=python-prod-worker"
wait_siphon_pcap_stack
ensure_netobserv_exports_to_collector

TRACE_HEX="$(openssl rand -hex 16)"
SPAN_HEX="$(openssl rand -hex 8)"
TRACE_TP="00-${TRACE_HEX}-${SPAN_HEX}-01"
ORDER_ID="e2e-$(openssl rand -hex 8)"

echo "==> Publish traced order (trace ${TRACE_HEX})"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
  rabbitmqadmin declare exchange name=egress-events type=topic durable=true 2>/dev/null || true
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"order_id\":\"${ORDER_ID}\"}' properties='{\"headers\":{\"traceparent\":\"${TRACE_TP}\"}}'
"
log_success "published traceparent-only message order_id=${ORDER_ID}"

echo "==> Wait for prod HTTP record (Siphon -> Recorder -> Beru)"
RECORDER_NS="$SHADOW_NS"
for i in $(seq 1 45); do
  if kubectl logs -n "$RECORDER_NS" "deploy/${SHADOWTEST}-recorder" --tail=120 2>/dev/null \
    | grep -Fq "recorded POST user-service.prod.internal/v1/log"; then
    log_success "Recorder seeded Beru mock for user-service.prod.internal/v1/log"
    break
  fi
  if [[ "$i" -eq 45 ]]; then
    log_fail "Recorder did not log HTTP seed (strict replay requires prod capture)"
    kubectl logs -n "$RECORDER_NS" "deploy/${SHADOWTEST}-recorder" --tail=40 >&2 || true
    kubectl logs -n siphon-system -l app.kubernetes.io/name=siphon-agent --tail=80 2>/dev/null \
      | grep -E 'siphon debug|egress outbound|egress inbound|egress relay' >&2 || true
    exit 1
  fi
  echo "    waiting for recorder seed (${i}/45)"
  sleep 2
done

echo "==> Wait for shadow workers to process message (HTTP via Envoy replay)"
wait_for_worker() {
  local role="$1"
  local pod
  for _ in $(seq 1 45); do
    pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
    if [[ -n "$pod" ]] && kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "order_id=${ORDER_ID}"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  if [[ -z "$pod" ]]; then
    log_fail "no shadow pod for role ${role}"
    exit 1
  fi
  if ! wait_for_worker "$role"; then
    log_fail "${role} missing order_id=${ORDER_ID} in logs"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  if kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "${TRACE_HEX}"; then
    log_fail "${role} logs contain trace hex (worker must be trace-unaware in body)"
    exit 1
  fi
  if ! kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "http egress via=replay status=200"; then
    log_fail "${role} missing http egress via=replay status=200 (NO_PROXY bypass or mock miss?)"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=30 >&2 || true
    exit 1
  fi
  log_success "${role} processed order + HTTP strict replay status=200"
done

echo "==> Wait for Beru dual count regressions (up to ${WAIT_SECS}s)"
beru_pod=$(kubectl get pods -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$beru_pod" ]]; then
  beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
fi
if [[ -z "$beru_pod" ]]; then
  log_fail "Beru pod not found"
  exit 1
fi

mongo_count_msg="Egress count regression for Trace ${TRACE_HEX} (mongodb): expected 1 query but got 2"
rmq_count_msg="Egress count regression for Trace ${TRACE_HEX} (rabbitmq): expected 1 message but got 2"

for i in $(seq 1 "$WAIT_SECS"); do
  beru_pod=$(kubectl get pods -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || beru_pod)
  logs=$(kubectl logs -n beru-system "$beru_pod" --tail=400 2>/dev/null || true)
  mongo_ok=0
  rmq_ok=0
  grep -qF "$mongo_count_msg" <<<"$logs" && mongo_ok=1
  grep -qF "$rmq_count_msg" <<<"$logs" && rmq_ok=1
  if [[ "$mongo_ok" == "1" && "$rmq_ok" == "1" ]]; then
    log_success "Beru flagged Mongo count regression for trace ${TRACE_HEX}"
    log_success "Beru flagged RabbitMQ count regression for trace ${TRACE_HEX}"
    trap - EXIT
    log_success "Python hybrid E2E passed (trace ${TRACE_HEX})"
    exit 0
  fi
  echo "    waiting (${i}/${WAIT_SECS}) mongo=${mongo_ok} rabbitmq=${rmq_ok}"
  sleep 1
done

log_fail "Beru logs missing dual count regressions after ${WAIT_SECS}s"
kubectl logs -n beru-system "$beru_pod" --tail=80 2>&1 | grep -E "${TRACE_HEX}|mongodb|rabbitmq|count regression|OTLP|Ingested" || kubectl logs -n beru-system "$beru_pod" --tail=40
exit 1
