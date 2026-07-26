# Traffic generation helpers — embed BATS_TRACE_TP / order IDs.
# shellcheck shell=bash

PROD_EXCHANGE="${PROD_EXCHANGE:-orders}"
PROD_ROUTING_KEY="${PROD_ROUTING_KEY:-order.created}"
HTTP_RECORD_HOST="${HTTP_RECORD_HOST:-user-service.prod.internal}"
HTTP_RECORD_PATH="${HTTP_RECORD_PATH:-/v1/log}"

publish_rmq_order() {
  local trace_id="${1:-${BATS_TRACE_ID}}"
  local order_id="${2:-${BATS_ORDER_ID}}"
  local span_hex="${BATS_SPAN_ID:-$(openssl rand -hex 8)}"
  local trace_tp="00-${trace_id}-${span_hex}-01"

  bats_source_e2e_helpers
  local broker_pod
  broker_pod="$(kubectl get pods -n default -l app=rmq-prod-broker \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
  if [[ -z "$broker_pod" ]]; then
    echo "publish_rmq_order: rmq-prod-broker pod not found (prod stack missing or not ready)" >&2
    return 1
  fi
  kubectl_exec_assert default "$broker_pod" "" \
    "rabbitmqadmin declare exchange name=${PROD_EXCHANGE} type=topic durable=true 2>/dev/null || true
     rabbitmqadmin declare exchange name=egress-events type=topic durable=true 2>/dev/null || true
     rabbitmqadmin publish exchange=${PROD_EXCHANGE} routing_key=${PROD_ROUTING_KEY} \
       payload='{\"order_id\":\"${order_id}\"}' properties='{\"headers\":{\"traceparent\":\"${trace_tp}\"}}'"
}

publish_igris_http() {
  local trace_id="${1:-${BATS_TRACE_ID}}"
  local body="${2:-{\"e2e\":\"bats-http-otel\"}}"
  local path="${3:-/publish}"
  local span_hex="${BATS_SPAN_ID:-$(openssl rand -hex 8)}"
  local trace_tp="00-${trace_id}-${span_hex}-01"
  local shadowtest="${SHADOWTEST}"
  local shadow_ns="${SHADOW_NS}"
  local url="http://${shadowtest}-igris.${shadow_ns}.svc.cluster.local:8888${path}"

  if ! kubectl get svc "${shadowtest}-igris" -n "$shadow_ns" >/dev/null 2>&1; then
    echo "publish_igris_http: no ${shadowtest}-igris service in ${shadow_ns}" >&2
    return 2
  fi
  bats_source_e2e_helpers
  local out
  out=$(kubectl run "bats-igris-${RANDOM}" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- \
    curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
    -X POST "$url" \
    -H "Content-Type: application/json" \
    -H "traceparent: ${trace_tp}" \
    -d "$body" 2>&1) || true
  out=$(e2e_strip_kubectl_run_output "$out")
  echo "$out"
  [[ "$out" == *'__HTTP_CODE__202'* ]]
}

multicast_igris_write() {
  publish_igris_http "$1" "$2" "/publish"
}

publish_prod_http() {
  local trace_id="${1:-${BATS_TRACE_ID}}"
  local body="${2:-{\"e2e\":\"bats-http-otel\"}}"
  local span_hex="${BATS_SPAN_ID:-$(openssl rand -hex 8)}"
  local trace_tp="00-${trace_id}-${span_hex}-01"
  local url="http://${PROD_DEPLOY}.default.svc.cluster.local:8080/publish"

  bats_source_e2e_helpers
  local out
  out=$(kubectl run "bats-prod-${RANDOM}" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- \
    curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
    -X POST "$url" \
    -H "Content-Type: application/json" \
    -H "traceparent: ${trace_tp}" \
    -d "$body" 2>&1) || true
  out=$(e2e_strip_kubectl_run_output "$out")
  echo "$out"
  [[ "$out" == *'__HTTP_CODE__200'* ]]
}

assert_worker_log_grep() {
  local role="$1" pattern="$2"
  local pod
  pod=$(bats_shadow_app_pod "$role")
  [[ -n "$pod" ]] || return 1
  local i
  for ((i = 1; i <= 45; i++)); do
    if kubectl logs -n "${SHADOW_NS}" "$pod" -c app --since=15m 2>/dev/null | grep -Fq "$pattern"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

assert_worker_trace_absent() {
  local role="$1" trace_id="${2:-${BATS_TRACE_ID}}"
  local pod
  pod=$(bats_shadow_app_pod "$role")
  [[ -n "$pod" ]] || return 1
  ! kubectl logs -n "${SHADOW_NS}" "$pod" -c app --since=15m 2>/dev/null | grep -q "$trace_id"
}

assert_worker_http_replay() {
  local role="$1" order_id="${2:-${BATS_ORDER_ID}}"
  assert_worker_log_grep "$role" "order_id=${order_id}" || return 1
  assert_worker_log_grep "$role" "http egress via=replay status=200"
}

bats_shadow_app_pod() {
  local role="$1"
  bats_source_e2e_helpers
  shadow_app_pod_for_role "${SHADOW_NS}" "${SHADOWTEST}" "$role"
}

assert_worker_processed_order() {
  local role="$1" order_id="$2"
  local pod
  pod=$(bats_shadow_app_pod "$role")
  [[ -n "$pod" ]] || return 1
  local i
  for ((i = 1; i <= 45; i++)); do
    if kubectl logs -n "${SHADOW_NS}" "$pod" -c app --since=15m 2>/dev/null | grep -Fq "order_id=${order_id}"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

# One prod RMQ publish + egress seed before hybrid @tests, so the first real
# assertion is not the one that pays for cold-start latency.
hybrid_egress_warmup() {
  [[ -n "${SHADOW_NS:-}" ]] || return 0
  local tid oid
  tid="warmup$(openssl rand -hex 6)"
  oid="bats-${tid:0:8}"
  echo "==> hybrid egress warmup"
  publish_rmq_order "$tid" "$oid" || return 0
  if wait_kaisel_egress_seed; then
    echo "    egress warmup ok"
  else
    echo "    WARNING: egress warmup timed out — HTTP replay tests may skip" >&2
  fi
}

assert_hybrid_workers_ready() {
  local order_id="${1:-${BATS_ORDER_ID}}"
  local role
  for role in control-a control-b candidate; do
    assert_worker_http_replay "$role" "$order_id" || return 1
  done
}

run_debug_mongo_egress() {
  "${REPO}/testing/bats/debug-mongo-egress.sh" "${SHADOWTEST}" "${SHADOWTEST_NS}"
}
