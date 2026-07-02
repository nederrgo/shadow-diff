#!/usr/bin/env bash
# E2E: MongoDB egress diffing — Pixie eBPF tcp_events → Beru OTLP → diff-of-diffs.
# Requires Minikube with a VM driver (kvm2/virtualbox) + Pixie Vizier + pixie-stream-bridge running.
# Set SKIP_BERU_MONGO_DIFF=1 to skip the Pixie/Beru assertion on Kind clusters.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-mongo-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
SHADOW_NS="${SHADOW_NS:-shadow-default-mongo-test-shadow}"
MONGO_TEST_IMG="${MONGO_TEST_IMG:-mongo-test-app:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
BERU_IMG="${BERU_IMG:-beru:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
WAIT_SECS="${WAIT_SECS:-40}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_BERU_BUILD="${SKIP_BERU_BUILD:-0}"
SKIP_BERU_MONGO_DIFF="${SKIP_BERU_MONGO_DIFF:-0}"
MONGO_IMAGE="${MONGO_IMAGE:-mongo:4.4}"

need() { require_cmd "$1"; }

strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container' | grep -v 'credentials and sensitive'
}

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

echo "==> Mongo egress E2E (Pixie eBPF → Beru OTLP)"
need kubectl
need docker
need openssl

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build mongo-test-app image"
  make -C "$REPO/testing/example-apps/mongo-test-app" docker-build MONGO_TEST_IMG="$MONGO_TEST_IMG"
fi

if [[ "$SKIP_BERU_BUILD" != "1" ]]; then
  echo "==> Build Beru image"
  make -C "$REPO/pipeline/beru" docker-build BERU_IMG="$BERU_IMG" 2>/dev/null || \
    bash "$REPO/testing/scripts/lib/docker.sh" build -t "$BERU_IMG" "$REPO/pipeline/beru"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  need kind
  [[ -n "${KIND_CLUSTER}" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
  kind load docker-image "$MONGO_TEST_IMG" --name "$KIND_CLUSTER"
  kind load docker-image "$BERU_IMG" --name "$KIND_CLUSTER"
  docker pull "$MONGO_IMAGE" 2>/dev/null || bash "$REPO/testing/scripts/lib/docker.sh" pull "$MONGO_IMAGE" 2>/dev/null || true
  kind load docker-image "$MONGO_IMAGE" --name "$KIND_CLUSTER" 2>/dev/null || true
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

kubectl apply -f "$REPO/pipeline/beru/deploy/deployment.yaml"
kubectl set image deployment/beru -n beru-system beru="$BERU_IMG" --record=false 2>/dev/null || true
kubectl rollout status deployment/beru -n beru-system --timeout=120s 2>/dev/null || true

kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true

kubectl apply -f "$REPO/testing/scripts/manifests/mongo-egress-e2e/prod-mongo-app.yaml"
kubectl rollout status deployment/mongo-test-prod -n default --timeout=180s

wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=true 2>/dev/null || true
wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180

kubectl apply -f "$REPO/testing/scripts/manifests/mongo-egress-e2e/shadowtest-mongo.yaml"

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

echo "==> Verify Envoy config has no L4 MongoDB listener (HTTP-only sidecar)"
for role in control-a control-b candidate; do
  cm="${SHADOWTEST}-${role}-envoy"
  yaml=$(kubectl get cm "$cm" -n "$SHADOW_NS" -o jsonpath='{.data.envoy\.yaml}' 2>/dev/null || true)
  for forbidden in mongo_egress mongo_upstream mongo_proxy; do
    if [[ "$yaml" == *"$forbidden"* ]]; then
      log_fail "Envoy CM ${cm} must not contain ${forbidden} (L4 MongoDB removed)"
      exit 1
    fi
  done
  log_success "Envoy CM ${cm}: no L4 MongoDB listener"
done

echo "==> Verify shadow apps get direct-service MONGO_URL (no Envoy proxy)"
for role in control-a control-b candidate; do
  got=$(kubectl get deploy "${SHADOWTEST}-${role}" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="app")].env[?(@.name=="MONGO_URL")].value}')
  expected_prefix="mongodb://mongo-${role}.${SHADOW_NS}.svc.cluster.local:"
  if [[ "$got" != "${expected_prefix}"* ]]; then
    log_fail "${SHADOWTEST}-${role}: expected MONGO_URL starting with ${expected_prefix}, got ${got:-<unset>}"
    exit 1
  fi
  log_success "${SHADOWTEST}-${role} MONGO_URL=${got}"
done

kubectl rollout status "deployment/${SHADOWTEST}-igris" -n "$SHADOW_NS" --timeout=120s

IGRIS_URL="http://${SHADOWTEST}-igris.${SHADOW_NS}.svc.cluster.local:8888"

TRACE_HEX="$(openssl rand -hex 16)"
SPAN_HEX="$(openssl rand -hex 8)"
TRACEPARENT="00-${TRACE_HEX}-${SPAN_HEX}-01"
echo "==> Test trace ${TRACE_HEX}"

echo "==> Multicast write via Igris (${IGRIS_URL}/write) traceparent=${TRACEPARENT}"
write_out=$(in_cluster_curl "mongo-e2e-write-${RANDOM}" \
  curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
  -X POST "${IGRIS_URL}/write" \
  -H "Content-Type: application/json" \
  -H "traceparent: ${TRACEPARENT}" \
  -d '{"id":"e2e","name":"test"}')
echo "    curl: $write_out"
if ! grep -q '__HTTP_CODE__202' <<<"$write_out"; then
  log_fail "Igris POST /write expected HTTP 202, got: ${write_out:-<empty>}"
  exit 1
fi
log_success "Igris accepted multicast (HTTP 202)"

echo "==> Wait for trace-unaware apps to insert (mongo insert ok)"
for role in control-a control-b candidate; do
  dep="${SHADOWTEST}-${role}"
  if ! shadow_app_logs "$SHADOW_NS" "$dep" app --tail=80 2>/dev/null | grep -q "mongo insert ok"; then
    log_fail "${role} app logs missing mongo insert ok"
    shadow_app_logs "$SHADOW_NS" "$dep" app --tail=30 >&2 || true
    exit 1
  fi
  log_success "${role} mongo insert ok"
done

if [[ "$SKIP_BERU_MONGO_DIFF" == "1" ]]; then
  echo "==> SKIP_BERU_MONGO_DIFF=1: skipping Pixie→Beru OTLP wait (Kind cluster)"
  echo "    To enable: run on Minikube with kvm2 driver + Pixie Vizier + pixie-stream-bridge"
  trap - EXIT
  log_success "Mongo egress E2E passed (structural checks only — Beru diff skipped)"
  exit 0
fi

echo "==> Wait for Beru OTLP mongo egress diff via Pixie (up to ${WAIT_SECS}s)"
echo "    Requires: pixie-stream-bridge running, PixieStreamRule active for ${SHADOW_NS}"
beru_pod=$(kubectl get pods -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$beru_pod" ]]; then
  beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
fi
if [[ -z "$beru_pod" ]]; then
  log_fail "Beru pod not found in beru-system"
  exit 1
fi

success_msg="No egress regression for Trace ${TRACE_HEX} (mongodb)"

for i in $(seq 1 "$WAIT_SECS"); do
  beru_pod=$(kubectl get pods -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "$beru_pod")
  logs=$(kubectl logs -n beru-system "$beru_pod" --tail=300 2>/dev/null || true)
  if grep -qF "$success_msg" <<<"$logs"; then
    log_success "Beru reported no mongo egress regression for trace ${TRACE_HEX}"
    trap - EXIT
    log_success "Mongo egress E2E passed (trace ${TRACE_HEX})"
    exit 0
  fi
  sleep 1
done

log_fail "Beru logs missing '${success_msg}' after ${WAIT_SECS}s"
echo "    Hint: verify pixie-stream-bridge is running and PixieStreamRule has mongoOtelEndpoint set"
kubectl get pixiestreamrule -A 2>/dev/null | grep -v "^NAME" || true
kubectl logs -n beru-system "$beru_pod" --tail=80 2>&1 | grep -E "${TRACE_HEX}|mongodb|OTLP|Ingested" || kubectl logs -n beru-system "$beru_pod" --tail=20
exit 1
