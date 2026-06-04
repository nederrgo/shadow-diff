#!/usr/bin/env bash
# E2E: Monarch ephemeral Redis dependencies — provision, inject REDIS_HOST, isolate, Igris multicast, Beru match.
#
# Prerequisites: Kind cluster with Monarch + Beru (+ Igris image loaded). Example:
#   ./scripts/e2e-reset-kind.sh --no-reset
#   ./examples/e2e-dependency-test.sh
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
# shellcheck source=scripts/lib/e2e-helpers.sh
source "$REPO/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-db-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
DB_TEST_IMG="${DB_TEST_IMG:-db-test-app:dev}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
IGRIS_IMG="${IGRIS_IMG:-igris:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
TRACE_ID="${TRACE_ID:-dep-e2e-$(date +%s)}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_CLEANUP="${SKIP_CLEANUP:-0}"

REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_IMAGE="${REDIS_IMAGE:-redis:7-alpine}"

need() { require_cmd "$1"; }

# kubectl run --rm prints "pod ... deleted" on stdout; keep only the remote command output.
strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container'
}

in_cluster_curl() {
  local name="$1"
  shift
  local out
  out=$(kubectl run "$name" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- "$@" 2>&1) || true
  strip_kubectl_run_output "$out"
}

diagnose_redis_deploy() {
  local shadow_ns="$1" name="$2"
  echo "    --- deployments in ${shadow_ns} ---" >&2
  kubectl get deploy -n "$shadow_ns" 2>/dev/null >&2 || true
  if kubectl get deploy "$name" -n "$shadow_ns" >/dev/null 2>&1; then
    kubectl describe deploy "$name" -n "$shadow_ns" 2>/dev/null | tail -25 >&2 || true
    kubectl get pods -n "$shadow_ns" -l "shadow-diff.io/resource-kind=dependency" 2>/dev/null >&2 || true
    local pod
    pod=$(kubectl get pods -n "$shadow_ns" -l "app.kubernetes.io/managed-by=monarch,shadow-diff.io/role=control-a,shadow-diff.io/resource-kind=dependency" \
      -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [[ -n "$pod" ]]; then
      kubectl describe pod "$pod" -n "$shadow_ns" 2>/dev/null | tail -30 >&2 || true
      kubectl logs "$pod" -n "$shadow_ns" -c dependency --tail=20 2>/dev/null >&2 || true
    fi
  fi
}

assert_deploy_ready() {
  local ns="$1" name="$2"
  local avail
  avail=$(kubectl get deploy "$name" -n "$ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
  if [[ "${avail:-0}" -lt 1 ]]; then
    log_fail "deployment $ns/$name not ready (availableReplicas=$avail)"
    kubectl describe deploy "$name" -n "$ns" | tail -20 >&2 || true
    exit 1
  fi
}

redis_cli() {
  local shadow_ns="$1" redis_svc="$2"
  shift 2
  local job="redis-cli-${RANDOM}"
  kubectl run "$job" --rm -i --restart=Never -n default \
    --image=redis:7-alpine -- \
    redis-cli -h "${redis_svc}.${shadow_ns}.svc.cluster.local" -p "$REDIS_PORT" "$@"
}

redis_get() {
  local shadow_ns="$1" redis_svc="$2" key="$3"
  local out
  out=$(strip_kubectl_run_output "$(redis_cli "$shadow_ns" "$redis_svc" GET "$key" 2>&1)" | tr -d '\r')
  echo "$out" | tail -1
}

redis_set() {
  local shadow_ns="$1" redis_svc="$2" key="$3" value="$4"
  redis_cli "$shadow_ns" "$redis_svc" SET "$key" "$value" >/dev/null
}

cleanup() {
  if [[ "$SKIP_CLEANUP" == "1" ]]; then
    echo "SKIP_CLEANUP=1 — leaving db-test resources"
    return 0
  fi
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=false
  kubectl delete -f "$REPO/tests/dependency-e2e/db-test-prod.yaml" --ignore-not-found --wait=false
}

# Apiserver only persists spec.dependencies when the live CRD schema defines it.
crd_api_accepts_dependencies() {
  local explain_out
  explain_out=$(kubectl explain shadowtest.spec.dependencies \
    --api-version=engine.shadow-diff.io/v1alpha1 2>&1) || true
  if echo "$explain_out" | grep -qiE "couldn't find|not find|does not exist|error"; then
    return 1
  fi
  echo "$explain_out" | grep -qi 'dependencies'
}

wait_crd_absent() {
  local i
  for i in $(seq 1 60); do
    if ! kubectl get crd shadowtests.engine.shadow-diff.io >/dev/null 2>&1; then
      return 0
    fi
    echo "    waiting for CRD removal (${i}/60)..."
    sleep 2
  done
  log_fail "timed out waiting for shadowtests.engine.shadow-diff.io CRD deletion"
  exit 1
}

ensure_shadowtest_crd() {
  local crd="$REPO/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  echo "==> Install ShadowTest CRD with spec.dependencies (fresh apply)"
  make -C "$REPO/monarch" manifests

  echo "    Removing ShadowTest CRs so CRD can be replaced cleanly"
  kubectl delete shadowtest --all --all-namespaces --ignore-not-found --wait=true 2>/dev/null || true

  if kubectl get crd shadowtests.engine.shadow-diff.io >/dev/null 2>&1; then
    echo "    Deleting existing ShadowTest CRD"
    kubectl delete crd shadowtests.engine.shadow-diff.io --wait=false
    wait_crd_absent
  fi

  kubectl apply -f "$crd"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s
  echo "    Waiting for apiserver to accept spec.dependencies on ShadowTest"
  local manifest="$REPO/tests/dependency-e2e/shadowtest-deps.yaml"
  local i
  for i in $(seq 1 24); do
    if kubectl apply --dry-run=server -f "$manifest" -o json 2>/dev/null \
      | grep -q '"dependencies"'; then
      break
    fi
    echo "    dry-run not accepting dependencies yet (${i}/24)..."
    sleep 2
  done

  if ! crd_api_accepts_dependencies; then
    log_fail "apiserver does not expose shadowtest.spec.dependencies after CRD install"
    kubectl explain shadowtest.spec --api-version=engine.shadow-diff.io/v1alpha1 2>&1 | head -30 >&2 || true
    exit 1
  fi
  log_success "ShadowTest CRD established (kubectl explain spec.dependencies OK)"
}

shadowtest_has_dependencies() {
  local raw
  raw=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.spec.dependencies[*].name}' 2>/dev/null || true)
  [[ -n "$raw" ]]
}

trap '[[ $? -ne 0 ]] && log_fail "dependency E2E failed (see above)"' EXIT

echo "==> Dependency E2E (trace ${TRACE_ID})"
need kubectl
need docker

if [[ "$SKIP_BUILD" != "1" ]]; then
  echo "==> Build db-test-app image"
  make -C "$REPO/examples/db-test-app" docker-build DB_TEST_IMG="$DB_TEST_IMG"
fi

if [[ "$SKIP_LOAD" != "1" ]]; then
  need kind
  [[ -n "${KIND_CLUSTER}" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
  echo "==> Load ${DB_TEST_IMG} into Kind (${KIND_CLUSTER})"
  kind load docker-image "$DB_TEST_IMG" --name "$KIND_CLUSTER"
  echo "==> Pre-load ${REDIS_IMAGE} for ephemeral Redis (avoids ImagePullBackOff on Kind)"
  docker pull "$REDIS_IMAGE"
  kind load docker-image "$REDIS_IMAGE" --name "$KIND_CLUSTER"
fi

echo "==> Verify Beru (and Monarch namespace)"
kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed — run: ./scripts/e2e-reset-kind.sh"
  exit 1
}

if [[ "$SKIP_MONARCH_BUILD" != "1" ]]; then
  echo "==> Build Monarch operator (${MONARCH_IMG})"
  if [[ "${MONARCH_NO_CACHE:-0}" == "1" ]]; then
    bash "$REPO/scripts/lib/docker.sh" build --no-cache -t "$MONARCH_IMG" "$REPO/monarch"
  else
    make -C "$REPO/monarch" docker-build IMG="$MONARCH_IMG"
  fi
fi

if [[ "$SKIP_LOAD" != "1" && "$SKIP_MONARCH_BUILD" != "1" ]]; then
  echo "==> Load ${MONARCH_IMG} into Kind (${KIND_CLUSTER})"
  kind load docker-image "$MONARCH_IMG" --name "$KIND_CLUSTER"
fi

if [[ "$SKIP_MONARCH_DEPLOY" != "1" ]]; then
  echo "==> Deploy Monarch operator"
  make -C "$REPO/monarch" deploy IMG="$MONARCH_IMG"
  # Same image tag (monarch:dev) does not change the Deployment spec after kind load;
  # restart pods so the node picks up the newly loaded image layers.
  if [[ "$SKIP_LOAD" != "1" ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
  log_success "Monarch rolled out ($(kubectl get deploy -n monarch-system monarch-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}'))"
else
  kubectl get deploy -n monarch-system monarch-controller-manager >/dev/null 2>&1 || {
    log_fail "Monarch not deployed — unset SKIP_MONARCH_DEPLOY or run: ./scripts/e2e-reset-kind.sh"
    exit 1
  }
fi

# make deploy also applies the CRD via kustomize; refresh afterward so schema is not stuck mid-delete.
ensure_shadowtest_crd

echo "==> Deploy prod app and ShadowTest"
kubectl apply -f "$REPO/tests/dependency-e2e/db-test-prod.yaml"

ST_MANIFEST="$REPO/tests/dependency-e2e/shadowtest-deps.yaml"
echo "==> Apply ShadowTest (server-side apply preserves spec.dependencies)"
kubectl apply --server-side --force-conflicts \
  --field-manager=dependency-e2e \
  -f "$ST_MANIFEST"

# Monarch may reconcile quickly; retry if spec was applied before CRD propagation.
for i in $(seq 1 12); do
  if shadowtest_has_dependencies; then
    break
  fi
  if [[ "$i" -eq 1 ]]; then
    echo "    spec.dependencies not visible yet, re-applying ShadowTest"
  fi
  kubectl apply --server-side --force-conflicts \
    --field-manager=dependency-e2e \
    -f "$ST_MANIFEST"
  sleep 2
done

if ! shadowtest_has_dependencies; then
  log_fail "ShadowTest spec.dependencies missing after apply"
  echo "    If last-applied-configuration lists dependencies but spec does not:" >&2
  echo "      1. Rebuild Monarch without cache: docker build --no-cache -t monarch:dev monarch/" >&2
  echo "      2. Ensure the running manager image matches your tree (not an old monarch:dev layer)" >&2
  kubectl explain shadowtest.spec.dependencies --api-version=engine.shadow-diff.io/v1alpha1 2>&1 >&2 || true
  kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o yaml 2>/dev/null | head -55 >&2 || true
  exit 1
fi
log_success "ShadowTest has spec.dependencies (redis)"

# Stale Ready from a pre-Phase-5a manager is common; re-reconcile after a fresh manager pod.
kubectl annotate shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
  "dependency-e2e.shadow-diff.io/reconcile-at=$(date +%s)" --overwrite 2>/dev/null || true
sleep 3
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
if [[ -n "$SHADOW_NS" ]] && ! kubectl get deploy redis-control-a -n "$SHADOW_NS" >/dev/null 2>&1; then
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  if [[ "$phase" == "Ready" ]]; then
    log_fail "ShadowTest is Ready but Monarch did not create redis-control-a (manager likely needs rollout restart)"
    echo "    Run: kubectl rollout restart deployment/monarch-controller-manager -n monarch-system" >&2
    echo "    Then delete/re-apply ShadowTest or re-run this script." >&2
    exit 1
  fi
fi

if [[ -n "$IGRIS_IMG" ]]; then
  kubectl patch shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --type=merge -p "$(cat <<EOF
{"spec":{"igris":{"image":"${IGRIS_IMG}","replicas":1}}}
EOF
)" 2>/dev/null || true
fi

kubectl rollout status deployment/db-test-prod -n default --timeout=180s
kubectl wait -n default --for=condition=Ready pod -l app=db-test-prod --timeout=120s

echo "==> Nudge Monarch reconcile"
kubectl annotate shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
  "dependency-e2e.shadow-diff.io/reconcile-at=$(date -Iseconds)" --overwrite 2>/dev/null || true

echo "==> Wait for ShadowTest Ready and Redis dependencies"
SHADOW_NS=""
phase=""
for i in $(seq 1 36); do
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  redis_ok=0
  if [[ -n "$SHADOW_NS" ]]; then
    if kubectl get deploy redis-control-a -n "$SHADOW_NS" >/dev/null 2>&1; then
      avail=$(kubectl get deploy redis-control-a -n "$SHADOW_NS" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
      [[ "${avail:-0}" -ge 1 ]] && redis_ok=1
    fi
  fi
  if [[ "$redis_ok" == "1" && "$phase" == "Ready" ]]; then
    break
  fi
  if [[ "$phase" == "Ready" && "$redis_ok" == "0" && -n "$SHADOW_NS" ]]; then
    echo "    WARN: status.phase=Ready but redis-control-a not available (stale status or crashing pods)"
  fi
  echo "    phase=$phase shadowNS=${SHADOW_NS:-<pending>} redis-control-a ready=${redis_ok} (${i}/36)"
  sleep 5
done

phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}')
SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
if [[ -z "$SHADOW_NS" ]]; then
  log_fail "ShadowTest has no shadowNamespace"
  kubectl describe shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >&2 || true
  exit 1
fi

avail=$(kubectl get deploy redis-control-a -n "$SHADOW_NS" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
if [[ "${avail:-0}" -lt 1 ]]; then
  log_fail "redis-control-a not ready in ${SHADOW_NS} (availableReplicas=${avail}, phase=${phase})"
  diagnose_redis_deploy "$SHADOW_NS" redis-control-a
  echo "    Common fixes:" >&2
  echo "      - Rebuild Monarch: make -C monarch docker-build IMG=${MONARCH_IMG} && kind load docker-image ${MONARCH_IMG}" >&2
  echo "      - Ensure spec.dependencies on ShadowTest: kubectl get shadowtest ${SHADOWTEST} -o yaml | grep -A6 dependencies:" >&2
  echo "      - Check Redis pod events above (ImagePullBackOff, CrashLoopBackOff)" >&2
  exit 1
fi

if [[ "$phase" != "Ready" ]]; then
  log_fail "ShadowTest phase=${phase} (expected Ready once Redis is up)"
  kubectl describe shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >&2 || true
  exit 1
fi

if ! kubectl get deploy redis-control-a -n "$SHADOW_NS" >/dev/null 2>&1; then
  monarch_img=$(kubectl get deploy -n monarch-system monarch-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo unknown)
  log_fail "redis-control-a not found in ${SHADOW_NS} — Monarch operator likely lacks Phase 5a (image=${monarch_img})"
  echo "    Re-run without SKIP_MONARCH_BUILD / SKIP_MONARCH_DEPLOY, or:" >&2
  echo "      make -C monarch docker-build IMG=${MONARCH_IMG} && kind load docker-image ${MONARCH_IMG} --name ${KIND_CLUSTER}" >&2
  echo "      make -C monarch deploy IMG=${MONARCH_IMG}" >&2
  kubectl get deploy -n "$SHADOW_NS" 2>/dev/null || true
  exit 1
fi
log_success "ShadowTest Ready (namespace=${SHADOW_NS})"

echo "==> Verify Monarch provisioned Redis dependencies"
for redis_dep in redis-control-a redis-control-b redis-candidate; do
  assert_deploy_ready "$SHADOW_NS" "$redis_dep"
  log_success "Redis deployment ${redis_dep} is available"
done

echo "==> Verify REDIS_HOST injection on shadow app pods"
for role in control-a control-b candidate; do
  deploy="${SHADOWTEST}-${role}"
  expected="redis-${role}.${SHADOW_NS}.svc.cluster.local:${REDIS_PORT}"
  got=$(kubectl get deploy "$deploy" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="app")].env[?(@.name=="REDIS_HOST")].value}')
  if [[ "$got" != "$expected" ]]; then
    log_fail "${deploy}: expected REDIS_HOST=${expected}, got ${got:-<unset>}"
    exit 1
  fi
  log_success "${deploy} REDIS_HOST=${got}"
done

echo "==> Wait for Igris and shadow app pods before multicast"
kubectl rollout status "deployment/${SHADOWTEST}-igris" -n "$SHADOW_NS" --timeout=120s
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

IGRIS_URL="http://${SHADOWTEST}-igris.${SHADOW_NS}.svc.cluster.local:8888"
echo "==> Multicast write via Igris (${IGRIS_URL}/store)"
# Igris responds 202 Accepted immediately; shadow JSON is verified via Redis below.
store_out=$(in_cluster_curl "dep-e2e-store-${TRACE_ID}" \
  curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
  -X POST "${IGRIS_URL}/store" \
  -H "Content-Type: application/json" \
  -H "x-shadow-trace-id: ${TRACE_ID}" \
  -d '{"key":"test-key","value":"test-value"}')
echo "    curl: $store_out"
if ! grep -q '__HTTP_CODE__202' <<<"$store_out"; then
  log_fail "Igris POST /store expected HTTP 202, got: ${store_out:-<empty>}"
  kubectl logs -n "$SHADOW_NS" "deploy/${SHADOWTEST}-igris" --tail=30 2>/dev/null >&2 || true
  exit 1
fi
log_success "Igris accepted multicast (HTTP 202)"

echo "==> Wait for shadow apps to write Redis and Beru to ingest"
sleep 8

echo "==> Verify test-key in each isolated Redis"
for redis_svc in redis-control-a redis-control-b redis-candidate; do
  val=$(redis_get "$SHADOW_NS" "$redis_svc" "test-key")
  if [[ "$val" != "test-value" ]]; then
    log_fail "${redis_svc}: GET test-key expected test-value, got '${val}'"
    exit 1
  fi
  log_success "${redis_svc} contains test-key=test-value"
done

echo "==> Verify write isolation (control-a-only not visible on control-b)"
redis_set "$SHADOW_NS" redis-control-a "control-a-only" "secret"
val_a=$(redis_get "$SHADOW_NS" redis-control-a "control-a-only")
if [[ "$val_a" != "secret" ]]; then
  log_fail "redis-control-a: SET control-a-only failed (got '${val_a}')"
  exit 1
fi
log_success "redis-control-a has control-a-only=secret"

val_b=$(redis_get "$SHADOW_NS" redis-control-b "control-a-only")
if [[ -n "$val_b" && "$val_b" != "(nil)" && "$val_b" != "" ]]; then
  log_fail "redis-control-b: expected empty/nil for control-a-only, got '${val_b}' (databases not isolated)"
  exit 1
fi
log_success "redis-control-b does not see control-a-only (isolated)"

echo "==> Verify Beru ingress diff"
if ! kubectl logs -n beru-system deploy/beru --tail=120 2>/dev/null | grep -q "No regression for Trace ${TRACE_ID}"; then
  log_fail "Beru logs missing 'No regression for Trace ${TRACE_ID}'"
  kubectl logs -n beru-system deploy/beru --tail=40 >&2 || true
  exit 1
fi
log_success "Beru reported no regression for trace ${TRACE_ID}"

cleanup
trap - EXIT

echo ""
log_success "Dependency E2E passed (trace ${TRACE_ID})"
