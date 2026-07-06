#!/usr/bin/env bash
# E2E: Node.js hybrid — RabbitMQ ingress + Mongo OTLP + HTTP record/replay + RMQ Firehose egress.
# Minikube only (Pixie HTTP record when pl namespace exists).
#
#   ./testing/scripts/e2e-nodejs-hybrid-test.sh
#   USE_PIXIE=1 ./testing/scripts/e2e-nodejs-hybrid-test.sh
#   SKIP_BUILD=1 SKIP_LOAD=1 ./testing/scripts/e2e-nodejs-hybrid-test.sh
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../../../.." && pwd)}"
# shellcheck source=testing/scripts/helpers/e2e-helpers.sh
source "$REPO/testing/scripts/helpers/e2e-helpers.sh"
# shellcheck source=testing/scripts/helpers/siphon-config.sh
source "$REPO/testing/scripts/helpers/siphon-config.sh"
# shellcheck source=testing/scripts/helpers/cluster-minikube.sh
source "$REPO/testing/scripts/helpers/cluster-minikube.sh"

trap '[[ $? -ne 0 ]] && log_fail "nodejs hybrid E2E failed (see above)"' EXIT

require_minikube
if ! minikube -p "${MINIKUBE_PROFILE:-minikube}" status --format='{{.Host}}' 2>/dev/null | grep -qi running; then
  log_fail "minikube profile ${MINIKUBE_PROFILE:-minikube} is not running — start with: ./testing/scripts/setup/e2e-reset-minikube.sh"
  exit 1
fi

SHADOWTEST="${SHADOWTEST:-nodejs-hybrid-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_NS="${SHADOW_NS:-shadow-default-nodejs-hybrid-shadow}"
NODEJS_HYBRID_WORKER_IMG="${NODEJS_HYBRID_WORKER_IMG:-nodejs-hybrid-worker:dev}"
IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
RECORDER_IMG="${RECORDER_IMG:-recorder:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
SHOP_IMG="${SHOP_IMG:-shop:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
MONGO_IMAGE="${MONGO_IMAGE:-mongo:4.4}"
WAIT_SECS="${WAIT_SECS:-45}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"
USE_PIXIE="${USE_PIXIE:-}"
HTTP_RECORD_HOST="${HTTP_RECORD_HOST:-user-service.prod.internal}"
HTTP_RECORD_PATH="${HTTP_RECORD_PATH:-/v1/log}"

if [[ -z "$USE_PIXIE" ]]; then
  if kubectl get ns pl >/dev/null 2>&1; then
    USE_PIXIE=1
  else
    USE_PIXIE=0
  fi
fi
if [[ "$USE_PIXIE" == "1" ]]; then
  # shellcheck source=testing/scripts/helpers/pixie-bridge.sh
  source "$REPO/testing/scripts/helpers/pixie-bridge.sh"
fi

e2e_load_image() {
  local img="$1"
  [[ "$SKIP_LOAD" == "1" ]] && return 0
  if [[ "${MINIKUBE_DRIVER:-kvm2}" == none ]]; then
    load_minikube_image "$img"
  else
    use_minikube_docker_env
    docker image inspect "$img" >/dev/null 2>&1 || {
      log_fail "missing image ${img} in minikube docker — build or unset SKIP_LOAD"
      exit 1
    }
  fi
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

echo "==> Node.js hybrid E2E (minikube pixie=${USE_PIXIE}; Mongo OTLP + HTTP replay + RMQ Firehose)"
require_kubectl_cluster
if [[ "$SKIP_BUILD" != "1" || "$SKIP_LOAD" != "1" ]]; then
  if [[ "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
  require_docker
fi

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build nodejs-hybrid-worker image"
  make -C "$REPO/testing/example-apps/nodejs-hybrid-worker" docker-build NODEJS_HYBRID_WORKER_IMG="$NODEJS_HYBRID_WORKER_IMG"
  echo "==> Build igris-rabbitmq image"
  make -C "$REPO/pipeline/igrises/igris-rabbitmq" docker-build IGRIS_RABBITMQ_IMG="$IGRIS_RABBITMQ_IMG"
  echo "==> Build egress-relay-rabbitmq image"
  make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="$EGRESS_RELAY_RABBITMQ_IMG"
  make -C "$REPO/pipeline/recorder" docker-build RECORDER_IMG="$RECORDER_IMG" 2>/dev/null || true
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  echo "==> Build Beru image"
  if [[ "${BERU_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/helpers/docker.sh" build --no-cache -t "$BERU_IMG" "$REPO/pipeline/beru"
  else
    make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
      bash "$REPO/testing/scripts/helpers/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
  fi
  echo "==> Build Shop image"
  make -C "$REPO/pipeline/shop" docker-build SHOP_IMG="$SHOP_IMG" 2>/dev/null || \
    bash "$REPO/testing/scripts/helpers/docker.sh" build -t "$SHOP_IMG" "$REPO/pipeline/shop"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  e2e_load_image "$NODEJS_HYBRID_WORKER_IMG"
  e2e_load_image "$IGRIS_RABBITMQ_IMG"
  e2e_load_image "$EGRESS_RELAY_RABBITMQ_IMG"
  e2e_load_image "$RECORDER_IMG"
  e2e_load_image "$BERU_IMG"
  e2e_load_image "$SHOP_IMG"
  docker pull "$MONGO_IMAGE" 2>/dev/null || bash "$REPO/testing/scripts/helpers/docker.sh" pull "$MONGO_IMAGE" 2>/dev/null || true
  e2e_load_image "$MONGO_IMAGE"
  docker pull rabbitmq:3-management-alpine 2>/dev/null || bash "$REPO/testing/scripts/helpers/docker.sh" pull rabbitmq:3-management-alpine 2>/dev/null || true
  e2e_load_image rabbitmq:3-management-alpine
fi

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/testing/scripts/helpers/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/pipeline/monarch"
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
    MONARCH_MODE=dev BERU_IMAGE="$BERU_IMG" SHOP_IMAGE="$SHOP_IMG"
  if [[ "$SKIP_LOAD" != "1" ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

upgrade_crd

echo "==> Deploy prod stack"
# Remove prod workers from other test suites that share the same 'orders' queue.
# delete-shadowtest.sh only cleans the shadow namespace; these deployments persist.
kubectl delete deployment python-prod-worker -n default --ignore-not-found --wait=false 2>/dev/null || true
kubectl apply -f "$REPO/testing/scripts/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
# Purge orphaned shadow-diff-* queues from previous runs (Monarch finalizer may have missed them).
# Each orphan gets a copy of every published message and competes with the prod worker.
kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=60s 2>/dev/null || true
kubectl exec -n default deploy/rmq-prod-broker -- sh -c "
  rabbitmqadmin list queues name -f tsv 2>/dev/null \
    | grep '^shadow-diff-' \
    | while read -r q; do rabbitmqadmin delete queue name=\"\$q\" 2>/dev/null && echo \"purged stale queue: \$q\"; done
" 2>/dev/null || true
kubectl apply -f "$MANIFEST_DIR/prod-mongo.yaml"
if kubectl get svc user-service -n prod -o jsonpath='{.spec.clusterIP}' 2>/dev/null | grep -qv '^None$'; then
  echo "==> Recreate user-service as headless (delete ClusterIP Service, keep Deployment)"
  kubectl delete svc user-service -n prod --ignore-not-found --wait=true
fi
kubectl apply -f "$MANIFEST_DIR/prod-user-service.yaml"
kubectl apply -f "$MANIFEST_DIR/prod-nodejs-worker.yaml"

kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
kubectl wait --for=condition=Available deployment/mongo-prod -n default --timeout=180s
kubectl wait --for=condition=Available deployment/user-service -n prod --timeout=180s
kubectl rollout restart deployment/nodejs-prod-worker -n default >/dev/null
kubectl rollout status deployment/nodejs-prod-worker -n default --timeout=120s

# Close ghost AMQP connections on the prod broker that don't belong to the live prod worker pod.
# Old pods built without heartbeat leave behind connections RabbitMQ never detects as dead.
echo "==> Evict ghost consumers from 'orders' queue"
LIVE_IP=$(kubectl get pod -n default -l app=nodejs-prod-worker \
  -o jsonpath='{.items[0].status.podIP}' 2>/dev/null || true)
if [[ -n "$LIVE_IP" ]]; then
  ghost_conns=$(kubectl exec -n default deploy/rmq-prod-broker -- \
    rabbitmqadmin list connections name peer_host -f tsv 2>/dev/null \
    | awk -F'\t' -v ip="$LIVE_IP" 'NR>1 && $2 != ip {print $1}')
  if [[ -n "$ghost_conns" ]]; then
    while IFS= read -r conn; do
      kubectl exec -n default deploy/rmq-prod-broker -- \
        rabbitmqadmin close connection name="$conn" 2>/dev/null \
        && echo "    closed ghost connection from old pod: $conn"
    done <<< "$ghost_conns"
  else
    echo "    no ghost connections (live pod=${LIVE_IP})"
  fi
else
  echo "    WARN: could not determine live pod IP, skipping ghost eviction"
fi

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$MANIFEST_DIR/shadowtest-nodejs-hybrid.yaml"

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

if [[ "$USE_PIXIE" == "1" ]]; then
  echo "==> HTTP egress record: Pixie OTLP -> Recorder"
else
  echo "==> HTTP egress record: no Pixie (set USE_PIXIE=1 on minikube+pl)"
fi
nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"

if [[ "$USE_PIXIE" == "1" ]]; then
  wait_pixie_vizier_pem 120
  wait_pixie_vizier_healthy 120
  wait_pixie_http_events_ready 180
  wait_pixie_stream_rule "$SHADOWTEST" "$SHADOWTEST_NS" 120
  echo "==> Restarting pixie-stream-bridge to pick up current PxL template"
  stop_pixie_stream_bridge
  start_pixie_stream_bridge_background 1
  kubectl apply -k "$REPO/testing/scripts/manifests/pixie-bridge/" >/dev/null
  echo "==> Waiting 35s for bridge first export cycle before publishing"
  sleep 35
fi

relay_deploy="${SHADOWTEST}-egress-relay-rabbitmq"
kubectl rollout status "deployment/${relay_deploy}" -n "$SHADOW_NS" --timeout=180s
kubectl rollout status "deployment/${SHADOWTEST}-igris-rabbitmq" -n "$SHADOW_NS" --timeout=120s
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

if [[ "$USE_PIXIE" == "1" ]]; then
  # Re-verify Pixie is healthy immediately before publishing. The shadow deployment
  # rollouts above can take 60-90s, during which the vizier may flap. The egress PxL
  # uses start_time='-30s', so the HTTP egress event must land while Pixie is healthy.
  echo "==> Re-verify Pixie healthy before publish (event must fall in -30s window)"
  wait_pixie_vizier_healthy 120
fi

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

echo "==> Wait for prod worker HTTP egress (record path for Pixie)"
for i in $(seq 1 45); do
  # --since captures across pod restarts; fall back to --tail for clusters without log aggregation
  PROD_LOGS="$(kubectl logs -n default deploy/nodejs-prod-worker --since=10m 2>/dev/null \
    || kubectl logs -n default deploy/nodejs-prod-worker --tail=200 2>/dev/null || true)"
  if echo "$PROD_LOGS" | grep -q "order_id=${ORDER_ID}" \
    && echo "$PROD_LOGS" | grep -q "http egress via=record status=200"; then
    log_success "prod worker recorded HTTP egress for order_id=${ORDER_ID}"
    break
  fi
  if [[ "$i" -eq 45 ]]; then
    log_fail "prod worker did not log HTTP record for order_id=${ORDER_ID}"
    echo "$PROD_LOGS" >&2
    echo "=== nodejs-prod-worker pod status ===" >&2
    kubectl get pods -n default -l app=nodejs-prod-worker -o wide >&2 || true
    echo "=== previous container logs (if crashed) ===" >&2
    kubectl logs -n default deploy/nodejs-prod-worker --previous --tail=40 2>&1 || echo "(no previous container)" >&2
    echo "=== RabbitMQ queue state ===" >&2
    kubectl exec -n default deploy/rmq-prod-broker -- rabbitmqadmin list queues name messages consumers 2>&1 || true
    echo "=== RabbitMQ bindings for 'orders' exchange ===" >&2
    kubectl exec -n default deploy/rmq-prod-broker -- rabbitmqadmin list bindings 2>&1 || true
    exit 1
  fi
  echo "    waiting for prod worker (${i}/45)"
  sleep 2
done

RECORD_MARKER="shop client: recorded POST ${HTTP_RECORD_HOST}${HTTP_RECORD_PATH}"
egress_pxl="${PIXIE_BRIDGE_STATE_DIR:-${REPO}/.cache/pixie-bridge}/${SHADOWTEST_NS}-pixie-${SHADOWTEST}-egress.pxl"

echo "==> Wait for prod HTTP record (Recorder -> Shop; host=${HTTP_RECORD_HOST})"
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
  local pod logs
  for _ in $(seq 1 45); do
    pod=$(shadow_app_pod_for_role "$SHADOW_NS" "$SHADOWTEST" "$role")
    if [[ -n "$pod" ]]; then
      logs=$(kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null || true)
      if echo "$logs" | grep -q "order_id=${ORDER_ID}" \
        && echo "$logs" | grep -q "http egress via=replay status=200"; then
        return 0
      fi
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
    log_fail "${role} missing order processing or HTTP replay for order_id=${ORDER_ID} (90s timeout)"
    kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=40 >&2 || true
    exit 1
  fi
  if kubectl logs -n "$SHADOW_NS" "$pod" -c app --tail=120 2>/dev/null | grep -q "${TRACE_HEX}"; then
    log_fail "${role} logs contain trace hex (worker must be trace-unaware in body)"
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
    log_success "Node.js hybrid E2E passed (trace ${TRACE_HEX})"
    echo "==> Left ShadowTest ${SHADOWTEST_NS}/${SHADOWTEST} (shadow namespace ${SHADOW_NS}) for inspection"
    echo "    Remove via Monarch: ./testing/scripts/setup/delete-shadowtest.sh ${SHADOWTEST} ${SHADOWTEST_NS}"
    exit 0
  fi
  echo "    waiting (${i}/${WAIT_SECS}) mongo=${mongo_ok} rabbitmq=${rmq_ok}"
  sleep 1
done

log_fail "Beru logs missing dual count regressions after ${WAIT_SECS}s"
kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=80 2>&1 | grep -E "${TRACE_HEX}|mongodb|rabbitmq|count regression|OTLP|Ingested" || kubectl logs -n "$SHADOW_NS" "$beru_pod" --tail=40
exit 1
