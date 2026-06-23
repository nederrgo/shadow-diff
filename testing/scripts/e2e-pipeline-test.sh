#!/usr/bin/env bash
# HTTP ingress debug: Igris -> Envoy ingress ext_proc -> Beru gRPC.
# Isolates Beru ingress (No regression for Trace) from egress/OTel paths.
# Kind or Minikube. Default fixture: db-test-shadow (no OTel).
#
# Usage:
#   ./testing/scripts/e2e-pipeline-test.sh
#   SKIP_BUILD=1 SKIP_LOAD=1 SKIP_MONARCH_DEPLOY=1 ./testing/scripts/e2e-pipeline-test.sh
#   SHADOWTEST=http-otel-rmq-nodejs-shadow MANIFEST_DIR=... ./testing/scripts/e2e-pipeline-test.sh
#
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
# shellcheck source=testing/scripts/lib/e2e-helpers.sh
source "$REPO/testing/scripts/lib/e2e-helpers.sh"

SHADOWTEST="${SHADOWTEST:-db-test-shadow}"
SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
MANIFEST_DIR="${MANIFEST_DIR:-$REPO/testing/scripts/manifests/dependency-e2e}"
PROD_MANIFEST="${PROD_MANIFEST:-$MANIFEST_DIR/db-test-prod.yaml}"
ST_MANIFEST="${ST_MANIFEST:-$MANIFEST_DIR/shadowtest-deps.yaml}"
APP_IMG="${APP_IMG:-db-test-app:dev}"
IGRIS_PATH="${IGRIS_PATH:-/store}"
MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_LOAD="${SKIP_LOAD:-0}"
SKIP_MONARCH_BUILD="${SKIP_MONARCH_BUILD:-0}"
SKIP_MONARCH_DEPLOY="${SKIP_MONARCH_DEPLOY:-0}"
SKIP_DEPLOY="${SKIP_DEPLOY:-0}"
WAIT_SECS="${WAIT_SECS:-45}"
TRACE_MODE="${TRACE_MODE:-both}" # legacy | w3c | both

BERU_GRPC="${BERU_GRPC:-beru.beru-system.svc.cluster.local:50051}"

cleanup() {
  [[ "${SKIP_CLEANUP:-0}" == "1" ]] && return 0
  echo "==> Cleanup (set SKIP_CLEANUP=1 to keep resources)"
  kubectl delete shadowtest "${SHADOWTEST}" -n "${SHADOWTEST_NS}" --ignore-not-found --wait=false
}
[[ "${SKIP_CLEANUP:-0}" == "1" ]] || trap cleanup EXIT

diag_section() { echo ""; echo "==> $*"; }

diag_envoy_beru_config() {
  local shadow_ns="$1" role="$2"
  local deploy cm beru_host beru_port
  deploy="${SHADOWTEST}-${role}"
  cm="${deploy}-envoy"
  if ! kubectl get cm "$cm" -n "$shadow_ns" >/dev/null 2>&1; then
    log_fail "missing ConfigMap ${cm} in ${shadow_ns}"
    return 1
  fi
  beru_host=$(kubectl get cm "$cm" -n "$shadow_ns" -o jsonpath='{.data.envoy\.yaml}' \
    | awk '/beru_ext_proc/{f=1} f&&/address:/{print $2; exit}')
  beru_port=$(kubectl get cm "$cm" -n "$shadow_ns" -o jsonpath='{.data.envoy\.yaml}' \
    | awk '/beru_ext_proc/{f=1} f&&/port_value:/{print $2; exit}')
  echo "    ${deploy}-envoy: beru_ext_proc -> ${beru_host:-?}:${beru_port:-?}"
  if ! kubectl get cm "$cm" -n "$shadow_ns" -o jsonpath='{.data.envoy\.yaml}' | grep -q 'envoy.filters.http.ext_proc'; then
    log_fail "Envoy config missing ingress ext_proc filter"
    return 1
  fi
  log_success "ingress ext_proc present; Beru cluster ${beru_host}:${beru_port}"
}

diag_beru_grpc() {
  local from_ns="${1:-default}"
  echo "    probe gRPC ${BERU_GRPC} from namespace ${from_ns}"
  local out
  out=$(kubectl run "ingress-grpc-${RANDOM}" --rm -i --restart=Never -n "$from_ns" \
    --image=fullstorydev/grpcurl:v1.8.9 -- \
    -plaintext -max-time 5 "${BERU_GRPC}" list 2>&1) || true
  if grep -qiE 'server does not support the reflection API|Failed to list services' <<<"$out"; then
    log_success "Beru gRPC reachable at ${BERU_GRPC} (no reflection — expected)"
    return 0
  fi
  if grep -q 'connection refused\|no such host\|context deadline exceeded\|Unavailable' <<<"$out"; then
    log_fail "Beru gRPC unreachable from ${from_ns}: ${out}"
    return 1
  fi
  log_success "Beru gRPC probe: ${out:-ok}"
}

diag_envoy_ext_proc_stats() {
  local shadow_ns="$1" pod="$2"
  echo "    envoy admin ext_proc stats (${pod}):"
  kubectl exec -n "$shadow_ns" "$pod" -c envoy-sidecar -- \
    wget -qO- http://127.0.0.1:9901/stats 2>/dev/null \
    | grep -E 'ext_proc|beru_ext_proc' | head -15 || echo "    (no ext_proc stats or wget unavailable)"
}

wait_shadowtest_ready() {
  local i phase msg shadow_ns
  for i in $(seq 1 60); do
    phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    msg=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    echo "    phase=${phase:-<none>} shadowNS=${shadow_ns:-<pending>} msg=${msg:-}" >&2
    [[ "$phase" == "Ready" && -n "$shadow_ns" ]] && { echo "$shadow_ns"; return 0; }
    [[ "$phase" == "Failed" ]] && {
      kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o yaml | tail -25 >&2
      return 1
    }
    sleep 5
  done
  return 1
}

multicast_igris() {
  local shadow_ns="$1" trace_id="$2" trace_tp="$3" use_w3c="$4"
  local igris_url="http://${SHADOWTEST}-igris.${shadow_ns}.svc.cluster.local:8888${IGRIS_PATH}"
  local hdr_args=()
  if [[ "$use_w3c" == "1" ]]; then
    hdr_args=(-H "traceparent: ${trace_tp}")
    echo "==> Multicast via Igris (W3C) ${igris_url} trace=${trace_id}"
  else
    hdr_args=(-H "x-shadow-trace-id: ${trace_id}")
    echo "==> Multicast via Igris (legacy header) ${igris_url} trace=${trace_id}"
  fi
  local out
  out=$(e2e_in_cluster_curl "ingress-e2e-${RANDOM}" \
    curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
    -X POST "${igris_url}" \
    -H "Content-Type: application/json" \
    "${hdr_args[@]}" \
    -d '{"key":"ingress-e2e","value":"probe"}')
  echo "    curl: $out"
  grep -q '__HTTP_CODE__202' <<<"$out" || {
    log_fail "Igris expected HTTP 202"
    kubectl logs -n "$shadow_ns" "deploy/${SHADOWTEST}-igris" --tail=30 >&2 || true
    return 1
  }
  log_success "Igris accepted multicast"
}

verify_app_saw_request() {
  local shadow_ns="$1"
  local role pod
  for role in control-a control-b candidate; do
    pod=$(shadow_app_pod_for_role "$shadow_ns" "$SHADOWTEST" "$role")
    if [[ -z "$pod" ]]; then
      log_fail "no app pod for role ${role}"
      return 1
    fi
    if ! kubectl get pod -n "$shadow_ns" "$pod" -o jsonpath='{.status.containerStatuses[?(@.name=="envoy-sidecar")].ready}' 2>/dev/null | grep -q true; then
      log_fail "${role}: envoy-sidecar not ready (pod=${pod})"
      return 1
    fi
    log_success "${role} app+envoy pod ready (${pod})"
  done
}

wait_beru_ingress() {
  local trace_id="$1"
  local beru_pod ingress_msg timeout_msg logs i
  ingress_msg="No regression for Trace ${trace_id}"
  timeout_msg="Timed out waiting for Trace ${trace_id} (INGRESS)"
  beru_pod=$(kubectl get pods -n beru-system -l app.kubernetes.io/name=beru -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  [[ -z "$beru_pod" ]] && beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
  [[ -z "$beru_pod" ]] && { log_fail "Beru pod not found"; return 1; }

  diag_section "Wait for Beru ingress (up to ${WAIT_SECS}s): ${ingress_msg}"
  for i in $(seq 1 "$WAIT_SECS"); do
    logs=$(kubectl logs -n beru-system "$beru_pod" --tail=400 2>/dev/null || true)
    if grep -qF "$ingress_msg" <<<"$logs"; then
      log_success "Beru ingress OK for trace ${trace_id}"
      return 0
    fi
    if grep -qF "$timeout_msg" <<<"$logs"; then
      log_fail "Beru INGRESS timeout (ext_proc did not get all 3 roles for trace ${trace_id})"
      grep -E "${trace_id}|INGRESS|payload not JSON|Could not diff" <<<"$logs" | tail -10 >&2 || true
      return 1
    fi
    if grep -qF "Could not diff trace: payload not JSON" <<<"$logs" && grep -qF "$trace_id" <<<"$logs"; then
      log_fail "Beru rejected non-JSON ingress payload for trace ${trace_id}"
      grep -E "${trace_id}|payload not JSON" <<<"$logs" | tail -5 >&2 || true
      return 1
    fi
    echo "    waiting (${i}/${WAIT_SECS})..." >&2
    sleep 1
  done
  log_fail "Beru missing '${ingress_msg}' after ${WAIT_SECS}s"
  echo "    recent Beru lines for trace:" >&2
  kubectl logs -n beru-system "$beru_pod" --tail=200 2>&1 | grep -E "${trace_id}|INGRESS|regression|ext_proc" | tail -15 >&2 \
    || kubectl logs -n beru-system "$beru_pod" --tail=30 >&2 || true
  return 1
}

run_one_trace() {
  local shadow_ns="$1" use_w3c="$2"
  local trace_id span_id trace_tp
  if [[ "$use_w3c" == "1" ]]; then
    trace_id="$(openssl rand -hex 16)"
    span_id="$(openssl rand -hex 8)"
    trace_tp="00-${trace_id}-${span_id}-01"
  else
    trace_id="ingress-e2e-$(date +%s)-${RANDOM}"
    trace_tp=""
  fi

  multicast_igris "$shadow_ns" "$trace_id" "$trace_tp" "$use_w3c"
  sleep 3
  if ! wait_beru_ingress "$trace_id"; then
    local pod
    pod=$(shadow_app_pod_for_role "$shadow_ns" "$SHADOWTEST" control-a)
    diag_section "Failure diagnostics (trace ${trace_id})"
    diag_envoy_ext_proc_stats "$shadow_ns" "$pod"
    kubectl logs -n "$shadow_ns" "$pod" -c envoy-sidecar --tail=40 2>&1 | grep -iE 'ext_proc|beru|error|upstream' | tail -15 >&2 \
      || kubectl logs -n "$shadow_ns" "$pod" -c envoy-sidecar --tail=20 >&2 || true
    return 1
  fi
  return 0
}

echo "==> HTTP ingress E2E / debug (ShadowTest=${SHADOWTEST})"
e2e_init_cluster "$REPO"
require_kubectl_cluster
[[ "$SKIP_BUILD" != "1" || "$SKIP_LOAD" != "1" ]] && require_docker

kubectl get deploy -n beru-system beru >/dev/null 2>&1 || {
  log_fail "Beru not deployed — run e2e-reset-kind.sh or e2e-reset-minikube.sh"
  exit 1
}

e2e_prepare_docker_build
if [[ "$SKIP_BUILD" != "1" ]]; then
  case "$SHADOWTEST" in
    http-otel-rmq-nodejs-shadow)
      make -C "$REPO/testing/example-apps/http-rmq-test-app" docker-build HTTP_RMQ_TEST_IMG="$APP_IMG"
      make -C "$REPO/pipeline/igrises/igris-http" docker-build IGRIS_IMG="${IGRIS_IMG:-igris-http:dev}"
      make -C "$REPO/pipeline/egress-relay-rabbitmq" docker-build EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
      ;;
    *)
      make -C "$REPO/testing/example-apps/db-test-app" docker-build DB_TEST_IMG="$APP_IMG"
      ;;
  esac
fi
if [[ "$SKIP_LOAD" != "1" ]]; then
  e2e_load_image "$APP_IMG"
  case "$SHADOWTEST" in
    http-otel-rmq-nodejs-shadow)
      e2e_load_image "${IGRIS_IMG:-igris-http:dev}"
      e2e_load_image "${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
      e2e_load_image rabbitmq:3-management-alpine
      ;;
    *)
      docker pull redis:7-alpine 2>/dev/null || true
      e2e_load_image redis:7-alpine 2>/dev/null || true
      ;;
  esac
fi

if [[ "$SKIP_MONARCH_DEPLOY" != "1" ]]; then
  [[ "$SKIP_MONARCH_BUILD" != "1" ]] && make -C "$REPO/pipeline/monarch" docker-build IMG="$MONARCH_IMG"
  [[ "$SKIP_LOAD" != "1" && "$SKIP_MONARCH_BUILD" != "1" ]] && e2e_load_image "$MONARCH_IMG"
  make -C "$REPO/pipeline/monarch" deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system MONARCH_MODE=dev
  [[ "$SKIP_LOAD" != "1" ]] && kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
fi

if [[ "$SKIP_DEPLOY" != "1" ]]; then
  kubectl apply -f "$REPO/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl apply -f "$PROD_MANIFEST"
  wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 120
  kubectl apply -f "$ST_MANIFEST"
fi

SHADOW_NS=$(wait_shadowtest_ready) || {
  log_fail "ShadowTest did not become Ready"
  exit 1
}
log_success "ShadowTest Ready namespace=${SHADOW_NS}"

if [[ "$SHADOWTEST" == http-otel-rmq-nodejs-shadow ]]; then
  bash "$REPO/testing/scripts/lib/apply-otel-instrumentation.sh" "$SHADOW_NS"
fi

kubectl rollout status "deployment/${SHADOWTEST}-igris" -n "$SHADOW_NS" --timeout=180s
for role in control-a control-b candidate; do
  kubectl rollout status "deployment/${SHADOWTEST}-${role}" -n "$SHADOW_NS" --timeout=180s
done

diag_section "Envoy -> Beru wiring"
diag_envoy_beru_config "$SHADOW_NS" control-a
diag_beru_grpc default
verify_app_saw_request "$SHADOW_NS"

fail=0
case "$TRACE_MODE" in
  legacy|header)
    run_one_trace "$SHADOW_NS" 0 || fail=1 ;;
  w3c|traceparent)
    run_one_trace "$SHADOW_NS" 1 || fail=1 ;;
  both|*)
    run_one_trace "$SHADOW_NS" 0 || fail=1
    run_one_trace "$SHADOW_NS" 1 || fail=1 ;;
esac

if [[ "$fail" != "0" ]]; then
  diag_section "Summary"
  echo "    Ingress path: Igris -> shadow :8888 -> Envoy ext_proc -> Beru gRPC ${BERU_GRPC}" >&2
  echo "    If apps are ready but Beru never logs INGRESS: ext_proc stream or trace-id on request headers." >&2
  echo "    If Beru logs INGRESS timeout: missing role reports (check envoy-sidecar logs per role)." >&2
  exit 1
fi

trap - EXIT
cleanup
log_success "HTTP ingress E2E passed (ShadowTest=${SHADOWTEST}, modes=${TRACE_MODE})"
