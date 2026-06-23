#!/usr/bin/env bash
# E2E: HTTP ingress (igris-http) → OTel → AMQP egress (Firehose) — Python zero-touch (flask+pika).
# Kind: kind load docker-image. Minikube: minikube docker-env or image load (auto-detected).
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/otel-bootstrap.sh
source "$REPO/testing/scripts/lib/otel-bootstrap.sh"
# shellcheck source=testing/scripts/lib/e2e-http-otel-rmq.sh
source "$REPO/testing/scripts/lib/e2e-http-otel-rmq.sh"

SHADOWTEST="${SHADOWTEST:-http-otel-rmq-python-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_NS="${SHADOW_NS:-shadow-default-http-otel-rmq-python-shadow}"
HTTP_RMQ_PYTHON_IMG="${HTTP_RMQ_PYTHON_IMG:-http-rmq-python-worker:dev}"
IGRIS_IMG="${IGRIS_IMG:-igris-http:dev}"
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

MANIFEST_DIR="$REPO/testing/scripts/manifests/http-otel-rmq-e2e"
RELAY_DEPLOY="${SHADOWTEST}-egress-relay-rabbitmq"
IGRIS_DEPLOY="${SHADOWTEST}-igris"

cleanup() {
  echo "==> Cleaning up HTTP OTel RMQ Python E2E resources..."
  kubectl delete shadowtest "${SHADOWTEST}" -n "${SHADOWTEST_NS}" --ignore-not-found --wait=false
  kubectl delete namespace "${SHADOW_NS}" --ignore-not-found=true 2>/dev/null || true
}
trap cleanup EXIT

echo "==> HTTP OTel RMQ E2E (Python)"
http_otel_rmq_init_cluster "$REPO"
require_kubectl_cluster
[[ "$SKIP_BUILD" != "1" || "$SKIP_LOAD" != "1" ]] && require_docker

if [[ "$SKIP_OTEL_BOOTSTRAP" != "1" ]]; then
  if ! otel_operator_ready 2>/dev/null; then
    echo "==> OpenTelemetry Operator not ready — running otel-bootstrap"
    install_otel_stack
  else
    echo "==> OpenTelemetry Operator already installed"
  fi
fi

kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed — run: ./testing/scripts/e2e-reset-kind.sh or ./testing/scripts/e2e-reset-minikube.sh"
  exit 1
}

http_otel_rmq_prepare_docker_build

if [[ "$SKIP_BUILD" != "1" ]]; then
  make -C "$REPO/testing/example-apps/http-rmq-python-worker" docker-build HTTP_RMQ_PYTHON_IMG="$HTTP_RMQ_PYTHON_IMG"
  make -C "$REPO/pipeline/igrises/igris-http" docker-build IGRIS_IMG="$IGRIS_IMG"
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  http_otel_rmq_load_image "$HTTP_RMQ_PYTHON_IMG"
  http_otel_rmq_load_image "$IGRIS_IMG"
  http_otel_rmq_load_image "$EGRESS_RELAY_RABBITMQ_IMG"
  http_otel_rmq_load_image "$BERU_IMG"
  docker pull rabbitmq:3-management-alpine 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" pull rabbitmq:3-management-alpine 2>/dev/null || true
  http_otel_rmq_load_image rabbitmq:3-management-alpine
fi

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
  make -C "$REPO/pipeline/monarch" docker-build IMG="$MONARCH_IMG"
fi

if [[ "$SKIP_LOAD" != "1" && "$SKIP_MONARCH_BUILD" != "1" ]]; then
  http_otel_rmq_load_image "$MONARCH_IMG"
fi

if [[ "$SKIP_MONARCH_DEPLOY" != "1" ]]; then
  make -C "$REPO/pipeline/monarch" deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev
  if [[ "$SKIP_LOAD" != "1" ]]; then
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

kubectl set image deployment/beru -n beru-system beru="$BERU_IMG" --record=false 2>/dev/null || true
kubectl rollout status deployment/beru -n beru-system --timeout=120s 2>/dev/null || true

http_otel_rmq_upgrade_crd "$REPO"

kubectl apply -f "$MANIFEST_DIR/prod-target-python.yaml"
kubectl rollout status deployment/http-rmq-python-prod -n default --timeout=120s

echo "==> Pre-provision Instrumentation CR in ${SHADOW_NS}"
bash "$REPO/testing/scripts/lib/apply-otel-instrumentation.sh" "$SHADOW_NS"

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$MANIFEST_DIR/shadowtest-python.yaml"

SHADOW_NS=$(http_otel_rmq_wait_shadowtest "$SHADOWTEST" "$SHADOWTEST_NS" "$RELAY_DEPLOY") || {
  log_fail "ShadowTest did not become Ready with egress-relay"
  exit 1
}
log_success "ShadowTest Ready namespace=${SHADOW_NS}"

bash "$REPO/testing/scripts/lib/apply-otel-instrumentation.sh" "$SHADOW_NS"

http_otel_rmq_verify_firehose "$SHADOW_NS"

kubectl rollout status "deployment/${RELAY_DEPLOY}" -n "$SHADOW_NS" --timeout=180s
kubectl rollout status "deployment/${IGRIS_DEPLOY}" -n "$SHADOW_NS" --timeout=120s
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

echo "==> Restart shadow apps so OTel webhook injects after Instrumentation CR exists"
for role in control-a control-b candidate; do
  kubectl rollout restart "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS"
done
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

echo "==> Assert OTel injection on shadow app pods"
chmod +x "$REPO/testing/scripts/assert-otel-injected.sh"
for role in control-a control-b candidate; do
  "$REPO/testing/scripts/assert-otel-injected.sh" "$SHADOW_NS" "$role" "$SHADOWTEST"
done

TRACE_HEX="$(openssl rand -hex 16)"
SPAN_HEX="$(openssl rand -hex 8)"
TRACE_TP="00-${TRACE_HEX}-${SPAN_HEX}-01"

http_otel_rmq_run_test "$SHADOWTEST" "$SHADOW_NS" "$IGRIS_DEPLOY" "$TRACE_HEX" "$TRACE_TP" \
  "rmq egress published exchange=egress-events"

trap - EXIT
cleanup
log_success "HTTP OTel RMQ Python E2E passed (trace ${TRACE_HEX})"
