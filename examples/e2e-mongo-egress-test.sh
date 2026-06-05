#!/usr/bin/env bash
# E2E: MongoDB egress diffing — Envoy mongo_proxy ALS → Beru diff-of-diffs.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
# shellcheck source=scripts/lib/e2e-helpers.sh
source "$REPO/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-mongo-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
MONGO_TEST_IMG="${MONGO_TEST_IMG:-mongo-test-app:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
IGRIS_IMG="${IGRIS_IMG:-igris:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
TRACE_ID="${TRACE_ID:-mongo-e2e-$(date +%s)}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"
# mongo:4.4 accepts legacy OP_INSERT; Envoy mongo_proxy does not decode OP_MSG (used by modern drivers).
MONGO_IMAGE="${MONGO_IMAGE:-mongo:4.4}"

need() { require_cmd "$1"; }

strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container' | grep -v 'credentials and sensitive'
}

# Shadow app pods share labels with mongo dependency pods (same role + shadowtest-name).
# Deployment log helpers can pick mongo-control-a instead of mongo-test-shadow-control-a.
shadow_app_pod() {
  local ns="$1" deploy_name="$2"
  kubectl get pods -n "$ns" --no-headers 2>/dev/null | awk -v p="${deploy_name}-" '$1 ~ "^" p {print $1; exit}'
}

shadow_app_logs() {
  local ns="$1" deploy_name="$2" container="$3"
  shift 3
  local pod
  pod=$(shadow_app_pod "$ns" "$deploy_name")
  if [[ -z "$pod" ]]; then
    return 1
  fi
  kubectl logs -n "$ns" "$pod" -c "$container" "$@"
}

in_cluster_curl() {
  local name="$1"
  shift
  local out
  out=$(kubectl run "$name" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- "$@" 2>&1) || true
  strip_kubectl_run_output "$out"
}

trap '[[ $? -ne 0 ]] && log_fail "mongo egress E2E failed (see above)"' EXIT

echo "==> Mongo egress E2E (trace ${TRACE_ID})"
need kubectl
need docker

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build mongo-test-app image"
  make -C "$REPO/examples/mongo-test-app" docker-build MONGO_TEST_IMG="$MONGO_TEST_IMG"
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  echo "==> Build Beru image"
  make -C "$REPO/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
    bash "$REPO/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/beru"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  need kind
  [[ -n "${KIND_CLUSTER}" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
  kind load docker-image "$MONGO_TEST_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  docker pull "$MONGO_IMAGE" 2>/dev/null || true
  kind load docker-image "$MONGO_IMAGE" --name "$KIND_CLUSTER" 2>/dev/null || true
fi

kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed — run: ./scripts/e2e-reset-kind.sh"
  exit 1
}

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/scripts/lib/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/monarch"
  else
    make -C "$REPO/monarch" docker-build IMG="$MONARCH_IMG"
  fi
fi

if [[ "$SKIP_LOAD" != "1" && "$SKIP_MONARCH_BUILD" != "1" ]]; then
  kind load docker-image "$MONARCH_IMG" --name "$KIND_CLUSTER"
fi

if [[ "$SKIP_MONARCH_DEPLOY" != "1" ]]; then
  make -C "$REPO/monarch" deploy IMG="$MONARCH_IMG"
  if [[ "$SKIP_LOAD" != "1" ]]; then
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

kubectl set image deployment/beru -n beru-system beru="$BERU_IMG" --record=false 2>/dev/null || true
kubectl rollout status deployment/beru -n beru-system --timeout=120s 2>/dev/null || true

kubectl apply -f "$REPO/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true

kubectl apply -f "$REPO/tests/mongo-egress-e2e/prod-mongo-app.yaml"
kubectl apply -f "$REPO/tests/mongo-egress-e2e/shadowtest-mongo.yaml"

if [[ -n "$IGRIS_IMG" ]]; then
  kubectl patch shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --type=merge -p "$(cat <<EOF
{"spec":{"igris":{"image":"${IGRIS_IMG}","replicas":1}}}
EOF
)" >/dev/null 2>&1 || true
fi

kubectl rollout status deployment/mongo-test-prod -n default --timeout=180s

echo "==> Wait for ShadowTest Ready and Mongo dependencies"
SHADOW_NS=""
for i in $(seq 1 48); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  mongo_ok=0
  if [[ -n "$SHADOW_NS" ]] && kubectl get deploy mongo-control-a -n "$SHADOW_NS" >/dev/null 2>&1; then
    avail=$(kubectl get deploy mongo-control-a -n "$SHADOW_NS" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    [[ "${avail:-0}" -ge 1 ]] && mongo_ok=1
  fi
  echo "    phase=${phase:-<none>} shadowNS=${SHADOW_NS:-<pending>} mongo-ready=${mongo_ok} (${i}/48)"
  if [[ "$phase" == "Ready" && "$mongo_ok" == "1" ]]; then
    break
  fi
  sleep 5
done

SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
if [[ -z "$SHADOW_NS" ]]; then
  log_fail "no shadowNamespace"
  exit 1
fi
log_success "ShadowTest Ready namespace=${SHADOW_NS}"

echo "==> Verify Envoy mongo_egress config"
for role in control-a control-b candidate; do
  cm="${SHADOWTEST}-${role}-envoy"
  yaml=$(kubectl get cm "$cm" -n "$SHADOW_NS" -o jsonpath='{.data.envoy\.yaml}' 2>/dev/null || true)
  for token in mongo_egress beru_als mongo_proxy envoy.access_loggers.tcp_grpc \
    TcpGrpcAccessLogConfig emit_dynamic_metadata; do
    if [[ "$yaml" != *"$token"* ]]; then
      log_fail "Envoy CM ${cm} missing ${token}"
      exit 1
    fi
  done
  if [[ "$yaml" == *transport_socket* ]]; then
    log_fail "Envoy CM ${cm} must not configure TLS transport_socket on mongo upstream"
    exit 1
  fi
done
log_success "Envoy mongo_egress + beru_als configured"

echo "==> Verify cleartext MONGO_URL on shadow apps"
for role in control-a control-b candidate; do
  got=$(kubectl get deploy "${SHADOWTEST}-${role}" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="app")].env[?(@.name=="MONGO_URL")].value}')
  if [[ "$got" != "mongodb://127.0.0.1:27017" ]]; then
    log_fail "${SHADOWTEST}-${role}: expected MONGO_URL=mongodb://127.0.0.1:27017, got ${got:-<unset>}"
    exit 1
  fi
  log_success "${SHADOWTEST}-${role} MONGO_URL=${got}"
done

kubectl rollout status "deployment/${SHADOWTEST}-igris" -n "$SHADOW_NS" --timeout=120s
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

IGRIS_URL="http://${SHADOWTEST}-igris.${SHADOW_NS}.svc.cluster.local:8888"
echo "==> Multicast write via Igris (${IGRIS_URL}/write)"
write_out=$(in_cluster_curl "mongo-e2e-write-${RANDOM}" \
  curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
  -X POST "${IGRIS_URL}/write" \
  -H "Content-Type: application/json" \
  -H "x-shadow-trace-id: ${TRACE_ID}" \
  -d '{"data":"e2e-mongo"}')
echo "    curl: $write_out"
if ! grep -q '__HTTP_CODE__202' <<<"$write_out"; then
  log_fail "Igris POST /write expected HTTP 202, got: ${write_out:-<empty>}"
  exit 1
fi
log_success "Igris accepted multicast (HTTP 202)"

echo "==> Wait for shadow workers and Beru mongo egress diff"
sleep 12

for role in control-a control-b candidate; do
  dep="${SHADOWTEST}-${role}"
  if ! shadow_app_logs "$SHADOW_NS" "$dep" app --tail=50 2>/dev/null | grep -q "trace=${TRACE_ID}"; then
    log_fail "${role} app logs missing trace=${TRACE_ID}"
    shadow_app_logs "$SHADOW_NS" "$dep" app --tail=30 >&2 || true
    exit 1
  fi
  log_success "${role} processed trace=${TRACE_ID}"
done

echo "==> Verify Beru mongo egress diff"
beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
if [[ -z "$beru_pod" ]]; then
  log_fail "Beru pod not found in beru-system"
  exit 1
fi
if ! kubectl logs -n beru-system "$beru_pod" --tail=120 | grep -q "No egress regression for Trace ${TRACE_ID} (mongodb)"; then
  log_fail "Beru logs missing 'No egress regression for Trace ${TRACE_ID} (mongodb)'"
  kubectl logs -n beru-system "$beru_pod" --tail=80 >&2 || true
  exit 1
fi
log_success "Beru reported no mongo egress regression for trace ${TRACE_ID}"

trap - EXIT
log_success "Mongo egress E2E passed (trace ${TRACE_ID})"
