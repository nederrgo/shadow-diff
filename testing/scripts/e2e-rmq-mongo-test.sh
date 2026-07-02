#!/usr/bin/env bash
# E2E: RMQ ingress (Igris traceparent) + Mongo write + RMQ egress — Beru ingress + rabbitmq egress diff.
# Kind or Minikube: auto-detect cluster; minikube builds use eval $(minikube docker-env).
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-rmq-mongo-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
RMQ_MONGO_WORKER_IMG="${RMQ_MONGO_WORKER_IMG:-rmq-mongo-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1 || true)}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"
WAIT_SECS="${WAIT_SECS:-45}"

need() { require_cmd "$1"; }

upgrade_crd() {
  kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true
}

beru_local_pod() {
  local shadow_ns="$1"
  kubectl get pods -n "$shadow_ns" -l app=beru-local --no-headers 2>/dev/null | awk '{print $1; exit}'
}

wait_beru_log() {
  local ns="$1" pod="$2" want="$3" wait_secs="$4"
  local logs i
  [[ -n "$pod" ]] || return 1
  for i in $(seq 1 "$wait_secs"); do
    logs=$(kubectl logs -n "$ns" "$pod" --tail=500 2>/dev/null || true)
    if grep -qF "$want" <<<"$logs"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

trap '[[ $? -ne 0 ]] && log_fail "RMQ+Mongo E2E failed (see above)"' EXIT

echo "==> RMQ+Mongo E2E"
e2e_init_cluster "$REPO"
require_kubectl_cluster
need kubectl
need openssl
[[ "$SKIP_BUILD" != "1" || "$SKIP_LOAD" != "1" ]] && require_docker

e2e_prepare_docker_build

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build rmq-mongo-worker"
  make -C "$REPO/testing/example-apps/rmq-mongo-worker" docker-build RMQ_MONGO_WORKER_IMG="$RMQ_MONGO_WORKER_IMG"
  make -C "$REPO/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="$IGRIS_RABBITMQ_IMG"
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  e2e_prepare_docker_build
  make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  e2e_prepare_docker_build
  e2e_load_image "$RMQ_MONGO_WORKER_IMG"
  e2e_load_image "$IGRIS_RABBITMQ_IMG"
  e2e_load_image "$EGRESS_RELAY_RABBITMQ_IMG"
  e2e_load_image "$BERU_IMG"
  docker pull rabbitmq:3-management-alpine 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" pull rabbitmq:3-management-alpine 2>/dev/null || true
  docker pull mongo:4.4 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" pull mongo:4.4 2>/dev/null || true
  e2e_load_image rabbitmq:3-management-alpine
  e2e_load_image mongo:4.4
fi

kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  echo "==> Note: beru-system not deployed; egress-relay uses per-shadow beru-local (default Monarch)"
}

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
  e2e_prepare_docker_build
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/lib/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/pipeline/monarch"
  else
    make -C "$REPO/pipeline/monarch" docker-build IMG="$MONARCH_IMG"
  fi
fi

if [[ "$SKIP_LOAD" != "1" && "$SKIP_MONARCH_BUILD" != "1" ]]; then
  e2e_load_image "$MONARCH_IMG"
fi

if [[ "$SKIP_MONARCH_DEPLOY" != "1" ]]; then
  make -C "$REPO/pipeline/monarch" deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev BERU_IMAGE="$BERU_IMG"
  if [[ "$SKIP_LOAD" != "1" ]]; then
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

kubectl set image deployment/beru -n beru-system beru="$BERU_IMG" --record=false 2>/dev/null || true
kubectl rollout status deployment/beru -n beru-system --timeout=120s 2>/dev/null || true

upgrade_crd

MANIFEST_DIR="$REPO/testing/scripts/manifests/rmq-mongo-e2e"
kubectl apply -f "$MANIFEST_DIR/prod-rabbitmq.yaml"
kubectl apply -f "$MANIFEST_DIR/prod-target.yaml"

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl apply -f "$MANIFEST_DIR/shadowtest-rmq-mongo.yaml"

kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
kubectl wait --for=condition=Available deployment/rmq-mongo-prod-target -n default --timeout=120s

echo "==> Wait for ShadowTest Ready"
SHADOW_NS=""
for i in $(seq 1 60); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  relay_ok=0
  mongo_ok=0
  if [[ -n "$SHADOW_NS" ]]; then
    avail=$(kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$SHADOW_NS" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
    if kubectl get deploy "mongodb-control-a" -n "$SHADOW_NS" >/dev/null 2>&1; then
      mavail=$(kubectl get deploy "mongodb-control-a" -n "$SHADOW_NS" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
      [[ "${mavail:-0}" -ge 1 ]] && mongo_ok=1
    fi
  fi
  echo "    phase=${phase:-<none>} queue=${queue:-<none>} relay=${relay_ok} mongo=${mongo_ok} (${i}/60)"
  if [[ "$phase" == "Ready" && -n "$queue" && "$relay_ok" == "1" && "$mongo_ok" == "1" ]]; then
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
[[ -n "$SHADOW_NS" ]] || { log_fail "no shadowNamespace"; exit 1; }
log_success "ShadowTest Ready namespace=${SHADOW_NS}"

wait_local_beru_rollout "$SHADOW_NS" 180s

for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

echo "==> Wait for Pixie CS_HEALTHY before sending traffic"
# shellcheck source=testing/scripts/lib/pixie-bridge.sh
source "$REPO/testing/scripts/lib/pixie-bridge.sh"
wait_pixie_vizier_healthy 180

TRACE_HEX="$(openssl rand -hex 16)"
SPAN_HEX="$(openssl rand -hex 8)"
TRACE_TP="00-${TRACE_HEX}-${SPAN_HEX}-01"

echo "==> Publish prod message traceparent=${TRACE_TP}"
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
  rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
    payload='{\"order_id\":\"e2e-1\"}' properties='{\"headers\":{\"traceparent\":\"${TRACE_TP}\"}}'
"
log_success "published prod AMQP message"

echo "==> Wait for shadow workers (${WAIT_SECS}s max)"
for role in control-a control-b candidate; do
  pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
  [[ -n "$pod" ]] || { log_fail "no pod for ${role}"; exit 1; }
  ok=0
  for _ in $(seq 1 "$WAIT_SECS"); do
    logs=$(kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=150 2>/dev/null || true)
    if grep -q "trace=${TRACE_HEX}" <<<"$logs" && \
       grep -q "mongo insert ok" <<<"$logs" && \
       grep -q "rmq egress published" <<<"$logs"; then
      ok=1
      break
    fi
    sleep 1
  done
  if [[ "$ok" != "1" ]]; then
    log_fail "${role} missing trace/mongo/egress logs for ${TRACE_HEX}"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=50 >&2 || true
    exit 1
  fi
  log_success "${role}: trace + mongo insert + rmq egress"
done

beru_local=$(beru_local_pod "$SHADOW_NS")
ingress_msg="No regression for Trace ${TRACE_HEX}"
egress_msg="No egress regression for Trace ${TRACE_HEX} (rabbitmq)"

echo "==> Verify Beru ingress (beru-local)"
if ! wait_beru_log "$SHADOW_NS" "$beru_local" "$ingress_msg" 60; then
  log_fail "Beru missing '${ingress_msg}' in ${SHADOW_NS}"
  kubectl logs -n "$SHADOW_NS" "$beru_local" --tail=80 >&2 || true
  exit 1
fi
log_success "Beru ingress: ${ingress_msg}"

echo "==> Verify Beru RabbitMQ egress (beru-local; egress-relay default without spec.beruGRPCAddress)"
if ! wait_beru_log "$SHADOW_NS" "$beru_local" "$egress_msg" 90; then
  log_fail "Beru missing '${egress_msg}' in ${SHADOW_NS}"
  kubectl logs -n "$SHADOW_NS" "$beru_local" --tail=80 >&2 || true
  kubectl logs -n "$SHADOW_NS" "deploy/${SHADOWTEST}-egress-relay-rabbitmq" --tail=40 >&2 || true
  exit 1
fi
log_success "Beru egress: ${egress_msg}"

mongo_msg="No egress regression for Trace ${TRACE_HEX} (mongodb)"
echo "==> Verify Beru MongoDB egress (beru-local; Pixie eBPF → OTLP :4317)"
if ! wait_beru_log "$SHADOW_NS" "$beru_local" "$mongo_msg" 120; then
  log_fail "Beru missing '${mongo_msg}' in ${SHADOW_NS}"
  echo "==> beru-local logs (last 80):" >&2
  kubectl logs -n "$SHADOW_NS" "$beru_local" --tail=80 >&2 || true
  echo "==> pixie-stream-bridge log:" >&2
  kubectl logs -n monarch-system -l app=pixie-stream-bridge --tail=40 >&2 || true
  exit 1
fi
log_success "Beru egress: ${mongo_msg}"

trap - EXIT
log_success "RMQ+Mongo E2E passed (trace ${TRACE_HEX})"
