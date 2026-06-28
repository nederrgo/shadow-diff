#!/usr/bin/env bash
# E2E: Ultimate hybrid — RabbitMQ ingress + Mongo OTLP + HTTP record/replay + RMQ Firehose egress.
# Candidate executes extra Mongo write + extra RMQ publish; Beru flags dual count regressions.
# Beru Step 5 Part 1: v2 engine mirrors legacy count-regression log strings during dual-write.
#
# Kind (default): builds + kind load docker-image
# Minikube + Pixie: auto when pl namespace exists; HTTP seed via Pixie egress OTLP -> Recorder
#
#   ./testing/scripts/e2e-python-hybrid-test.sh
#   USE_PIXIE=1 ./testing/scripts/e2e-python-hybrid-test.sh   # force Pixie HTTP seed
#   SKIP_BUILD=1 SKIP_LOAD=1 ./testing/scripts/e2e-python-hybrid-test.sh  # images already in cluster
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"
# shellcheck source=testing/scripts/lib/otel-bootstrap.sh
source "$REPO/testing/scripts/lib/otel-bootstrap.sh"
# shellcheck source=testing/scripts/lib/siphon-config.sh
source "$REPO/testing/scripts/lib/siphon-config.sh"

detect_e2e_cluster() {
  if minikube -p "${MINIKUBE_PROFILE:-minikube}" status --format='{{.Host}}' 2>/dev/null | grep -qi running; then
    echo minikube
    return
  fi
  if kind get clusters 2>/dev/null | grep -q .; then
    echo kind
    return
  fi
  echo unknown
}

E2E_CLUSTER="${E2E_CLUSTER:-$(detect_e2e_cluster)}"
if [[ "$E2E_CLUSTER" == minikube ]]; then
  # shellcheck source=testing/scripts/lib/cluster-minikube.sh
  source "$REPO/testing/scripts/lib/cluster-minikube.sh"
fi

SHADOWTEST="${SHADOWTEST:-python-hybrid-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_NS="${SHADOW_NS:-shadow-default-python-hybrid-shadow}"
PYTHON_TEST_WORKER_IMG="${PYTHON_TEST_WORKER_IMG:-python-test-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
RECORDER_IMG="${RECORDER_IMG:-recorder:dev}"
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
USE_PIXIE="${USE_PIXIE:-}"
HTTP_RECORD_HOST="${HTTP_RECORD_HOST:-user-service.prod.internal}"
HTTP_RECORD_PATH="${HTTP_RECORD_PATH:-/v1/log}"

if [[ -z "$USE_PIXIE" ]]; then
  if [[ "$E2E_CLUSTER" == minikube ]] && kubectl get ns pl >/dev/null 2>&1; then
    USE_PIXIE=1
  else
    USE_PIXIE=0
  fi
fi
if [[ "$USE_PIXIE" == "1" ]]; then
  # shellcheck source=testing/scripts/lib/pixie-bridge.sh
  source "$REPO/testing/scripts/lib/pixie-bridge.sh"
  # shellcheck source=testing/scripts/lib/siphon-otlp.sh
  source "$REPO/testing/scripts/lib/siphon-otlp.sh"
fi

e2e_load_image() {
  local img="$1"
  [[ "$SKIP_LOAD" == "1" ]] && return 0
  case "$E2E_CLUSTER" in
    minikube)
      if [[ "${MINIKUBE_DRIVER:-kvm2}" == none ]]; then
        load_minikube_image "$img"
      else
        use_minikube_docker_env
        docker image inspect "$img" >/dev/null 2>&1 || {
          log_fail "missing image ${img} in minikube docker — build or unset SKIP_LOAD"
          exit 1
        }
      fi
      ;;
    kind)
      require_cmd kind
      [[ -n "$KIND_CLUSTER" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
      kind load docker-image "$img" --name "$KIND_CLUSTER"
      ;;
    *)
      log_fail "need kind or minikube cluster (detected: ${E2E_CLUSTER})"
      exit 1
      ;;
  esac
}

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"
MANIFEST_DIR="$REPO/testing/scripts/manifests/rabbitmq-otel-e2e"
SHADOW_WAIT_LOOPS="${SHADOW_WAIT_LOOPS:-90}"

hybrid_beru_pod_name() {
  local shadow_ns="$1"
  kubectl get pods -n "$shadow_ns" -l app=beru-local \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null \
    || kubectl get pods -n "$shadow_ns" --no-headers 2>/dev/null | awk '/^beru-local-/{print $1; exit}'
}

upgrade_crd() {
  make -C "$REPO/pipeline/monarch" install
}

trap '[[ $? -ne 0 ]] && log_fail "python hybrid E2E failed (see above)"' EXIT

echo "==> Python hybrid E2E (cluster=${E2E_CLUSTER} pixie=${USE_PIXIE}; Mongo OTLP + HTTP replay + RMQ Firehose)"
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

if [[ "$SKIP_BUILD" != "1" ]]; then
  if [[ "$E2E_CLUSTER" == minikube && "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
  echo "==> Build python-test-worker image"
  make -C "$REPO/testing/example-apps/python-test-worker" docker-build PYTHON_TEST_WORKER_IMG="$PYTHON_TEST_WORKER_IMG"
  echo "==> Build igris-rabbitmq image"
  make -C "$REPO/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="$IGRIS_RABBITMQ_IMG"
  echo "==> Build egress-relay-rabbitmq image"
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
  make -C "$REPO/pipeline/recorder" docker-build RECORDER_IMG="$RECORDER_IMG" 2>/dev/null || true
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
  e2e_load_image "$PYTHON_TEST_WORKER_IMG"
  e2e_load_image "$IGRIS_RABBITMQ_IMG"
  e2e_load_image "$EGRESS_RELAY_RABBITMQ_IMG"
  e2e_load_image "$RECORDER_IMG"
  e2e_load_image "$BERU_IMG"
  docker pull "$MONGO_IMAGE" 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull "$MONGO_IMAGE" 2>/dev/null || true
  e2e_load_image "$MONGO_IMAGE"
  docker pull rabbitmq:3-management-alpine 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull rabbitmq:3-management-alpine 2>/dev/null || true
  e2e_load_image rabbitmq:3-management-alpine
fi

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
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
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev BERU_IMAGE="$BERU_IMG"
  if [[ "$SKIP_LOAD" != "1" ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

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

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$MANIFEST_DIR/shadowtest-python-hybrid.yaml"

echo "==> Wait for ShadowTest Ready"
SHADOW_NS=""
shadow_ready=0
for i in $(seq 1 "$SHADOW_WAIT_LOOPS"); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  actual_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  siphon=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}' 2>/dev/null || true)
  relay_ok=0
  mongo_ok=0
  rabbitmq_ok=0
  if [[ -n "$actual_ns" ]] && kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$actual_ns" >/dev/null 2>&1; then
    avail=$(kubectl get deploy "${SHADOWTEST}-egress-relay-rabbitmq" -n "$actual_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
  fi
  if [[ -n "$actual_ns" ]] && kubectl get deploy mongodb-control-a -n "$actual_ns" >/dev/null 2>&1; then
    avail=$(kubectl get deploy mongodb-control-a -n "$actual_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && mongo_ok=1
  fi
  if [[ -n "$actual_ns" ]] && kubectl get deploy rabbitmq-control-a -n "$actual_ns" >/dev/null 2>&1; then
    avail=$(kubectl get deploy rabbitmq-control-a -n "$actual_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && rabbitmq_ok=1
  fi
  echo "    phase=${phase:-<none>} msg=${message:-<none>} queue=${queue:-<none>} siphon=${siphon:-<none>} shadowNS=${actual_ns:-<pending>} relay=${relay_ok} mongo=${mongo_ok} rabbitmq=${rabbitmq_ok} (${i}/${SHADOW_WAIT_LOOPS})"
  if [[ "$phase" == "Ready" && -n "$queue" && "$relay_ok" == "1" && "$mongo_ok" == "1" && "$rabbitmq_ok" == "1" && "$siphon" == "Ready" ]]; then
    SHADOW_NS="$actual_ns"
    shadow_ready=1
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
phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
if [[ -z "$SHADOW_NS" || "$shadow_ready" != "1" || "$phase" != "Ready" ]]; then
  log_fail "ShadowTest not Ready (phase=${phase:-<none>} shadowNS=${SHADOW_NS:-<none>})"
  kubectl get pods -n "${SHADOW_NS:-default}" 2>/dev/null | sed 's/^/       /' >&2 || true
  exit 1
fi
log_success "ShadowTest Ready namespace=${SHADOW_NS} siphonPhase=Ready"

wait_local_beru_rollout "$SHADOW_NS"

chmod +x "$REPO/testing/scripts/assert-otel-injected.sh"
for role in control-a control-b candidate; do
  "$REPO/testing/scripts/assert-otel-injected.sh" "$SHADOW_NS" "$role" "$SHADOWTEST"
done

if [[ "$USE_PIXIE" == "1" ]]; then
  echo "==> HTTP egress record: Pixie OTLP -> Recorder"
else
  echo "==> HTTP egress record: no Pixie (legacy Siphon removed; set USE_PIXIE=1 on minikube+pl)"
fi
nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"

if [[ "$USE_PIXIE" == "1" ]]; then
  wait_pixie_vizier_pem 120
  wait_pixie_vizier_healthy 120
  wait_pixie_http_events_ready 180
  wait_pixie_stream_rule "$SHADOWTEST" "$SHADOWTEST_NS" 120
  if ! pgrep -f pixie-stream-bridge.sh >/dev/null 2>&1; then
    start_pixie_stream_bridge_background
  fi
  kubectl apply -k "$REPO/testing/scripts/manifests/pixie-bridge/" >/dev/null
fi

relay_deploy="${SHADOWTEST}-egress-relay-rabbitmq"
kubectl rollout status "deployment/${relay_deploy}" -n "$SHADOW_NS" --timeout=180s
kubectl rollout status "deployment/${SHADOWTEST}-igris-rabbitmq" -n "$SHADOW_NS" --timeout=120s

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

RECORD_MARKER="beru client: recorded POST ${HTTP_RECORD_HOST}${HTTP_RECORD_PATH}"
egress_pxl="${PIXIE_BRIDGE_STATE_DIR:-${REPO}/.cache/pixie-bridge}/${SHADOWTEST_NS}-pixie-${SHADOWTEST}-egress.pxl"

echo "==> Wait for prod HTTP record (Recorder -> Beru; host=${HTTP_RECORD_HOST})"
RECORDER_NS="$SHADOW_NS"
for i in $(seq 1 60); do
  if [[ "$USE_PIXIE" == "1" ]] && [[ -f "$egress_pxl" ]] && pixie_vizier_healthy; then
    run_pixie_export_once "$egress_pxl" || true
  fi
  if kubectl logs -n "$RECORDER_NS" "deploy/${SHADOWTEST}-recorder" --tail=200 2>/dev/null \
    | grep -Fq "$RECORD_MARKER"; then
    log_success "Recorder seeded Beru mock for ${HTTP_RECORD_HOST}${HTTP_RECORD_PATH}"
    break
  fi
  if [[ "$i" -eq 60 ]]; then
    log_fail "Recorder did not log HTTP seed (need Pixie egress or manual Beru seed)"
    kubectl logs -n "$RECORDER_NS" "deploy/${SHADOWTEST}-recorder" --tail=40 >&2 || true
    if [[ "$USE_PIXIE" == "1" ]]; then
      px get viziers 2>&1 | sed 's/^/       /' >&2 || true
      grep -A2 req_host "$egress_pxl" 2>/dev/null | sed 's/^/       /' >&2 || true
    fi
    exit 1
  fi
  echo "    waiting for recorder seed (${i}/60)"
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
beru_pod=$(hybrid_beru_pod_name "$SHADOW_NS")
if [[ -z "$beru_pod" ]]; then
  log_fail "beru-local pod not found in ${SHADOW_NS}"
  exit 1
fi

mongo_count_msg="Egress count regression for Trace ${TRACE_HEX} (mongodb): expected 1 query but got 2"
rmq_count_msg="Egress count regression for Trace ${TRACE_HEX} (rabbitmq): expected 1 message but got 2"

for i in $(seq 1 "$WAIT_SECS"); do
  beru_pod=$(hybrid_beru_pod_name "$SHADOW_NS")
  logs=$(kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=400 2>/dev/null || true)
  mongo_ok=0
  rmq_ok=0
  grep -qF "$mongo_count_msg" <<<"$logs" && mongo_ok=1
  grep -qF "$rmq_count_msg" <<<"$logs" && rmq_ok=1
  if [[ "$mongo_ok" == "1" && "$rmq_ok" == "1" ]]; then
    log_success "Beru flagged Mongo count regression for trace ${TRACE_HEX}"
    log_success "Beru flagged RabbitMQ count regression for trace ${TRACE_HEX}"
    log_success "Python hybrid E2E passed (trace ${TRACE_HEX})"
    echo "==> Left ShadowTest ${SHADOWTEST_NS}/${SHADOWTEST} (shadow namespace ${SHADOW_NS}) for inspection"
    echo "    Remove via Monarch: ./testing/scripts/delete-shadowtest.sh ${SHADOWTEST} ${SHADOWTEST_NS}"
    exit 0
  fi
  echo "    waiting (${i}/${WAIT_SECS}) mongo=${mongo_ok} rabbitmq=${rmq_ok}"
  sleep 1
done

log_fail "Beru logs missing dual count regressions after ${WAIT_SECS}s"
kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=80 2>&1 | grep -E "${TRACE_HEX}|mongodb|rabbitmq|count regression|OTLP|Ingested" || kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=40
exit 1
