#!/usr/bin/env bash
# Shared helpers for HTTP ingress → RabbitMQ egress E2E scripts.
# Minikube + kvm2 only — no Kind, no cluster detection.
# shellcheck shell=bash

http_otel_rmq_init_cluster() {
  local repo="$1"
  # shellcheck source=testing/bats/helpers/cluster-minikube.sh
  source "$repo/testing/bats/helpers/cluster-minikube.sh"
  echo "==> E2E cluster: minikube (kvm2)"
}

http_otel_rmq_prepare_docker_build() {
  use_minikube_docker_env
}

http_otel_rmq_load_image() {
  local img="$1"
  [[ "${SKIP_LOAD:-0}" == "1" ]] && return 0
  docker image inspect "$img" >/dev/null 2>&1 || {
    log_fail "missing image ${img} in minikube docker — build or unset SKIP_LOAD"
    exit 1
  }
}

http_otel_rmq_strip_kubectl_run_output() {
  local out="$1"
  echo "$out" | grep -v '^pod "' | grep -v '^If you don' | grep -v '^All commands' | grep -v '^Defaulted container' | grep -v 'credentials and sensitive'
}

http_otel_rmq_in_cluster_curl() {
  local name="$1"
  shift
  local out
  out=$(kubectl run "$name" --rm -i --restart=Never -n default \
    --image=curlimages/curl:latest -- "$@" 2>&1) || true
  http_otel_rmq_strip_kubectl_run_output "$out"
}

http_otel_rmq_upgrade_crd() {
  local repo="$1"
  kubectl apply -f "$repo/pipeline/monarch/config/crd/bases/engine.shadow-diff.io_shadowtests.yaml"
  kubectl wait --for=condition=Established crd/shadowtests.engine.shadow-diff.io --timeout=120s 2>/dev/null || true
}

http_otel_rmq_dump_shadowtest_blockers() {
  local shadow_ns="$1"
  [[ -n "$shadow_ns" ]] || return 0
  echo "==> Shadow namespace deployments:" >&2
  kubectl get deploy -n "$shadow_ns" 2>/dev/null >&2 || true
  echo "==> Pods not Running:" >&2
  kubectl get pods -n "$shadow_ns" --field-selector=status.phase!=Running 2>/dev/null >&2 || true
}

http_otel_rmq_wait_shadowtest() {
  local shadowtest="$1" shadowtest_ns="$2" relay_deploy="$3"
  local shadow_ns="" i phase relay_ok avail msg
  local wait_loops="${HTTP_OTEL_RMQ_SHADOW_WAIT:-60}"
  for i in $(seq 1 "$wait_loops"); do
    phase=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    msg=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.message}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    relay_ok=0
    if [[ -n "$shadow_ns" ]] && kubectl get deploy "$relay_deploy" -n "$shadow_ns" >/dev/null 2>&1; then
      avail=$(kubectl get deploy "$relay_deploy" -n "$shadow_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
      [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
    fi
    echo "    phase=${phase:-<none>} msg=${msg:-<none>} shadowNS=${shadow_ns:-<pending>} egress-relay-ready=${relay_ok} (${i}/${wait_loops})" >&2
    if [[ "$phase" == "Ready" && -n "$shadow_ns" && "$relay_ok" == "1" ]]; then
      echo "$shadow_ns"
      return 0
    fi
    if [[ "$phase" == "Failed" ]]; then
      kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o yaml | tail -30 >&2
      http_otel_rmq_dump_shadowtest_blockers "$shadow_ns"
      return 1
    fi
    sleep 5
  done
  http_otel_rmq_dump_shadowtest_blockers "$shadow_ns"
  return 1
}

http_otel_rmq_verify_firehose() {
  local shadow_ns="$1"
  local role dep plugins_env probe
  for role in control-a control-b candidate; do
    dep="rabbitmq-${role}"
    plugins_env=$(kubectl get deploy "$dep" -n "$shadow_ns" \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="dependency")].env[?(@.name=="RABBITMQ_ENABLED_PLUGINS_FILE")].value}')
    if [[ "$plugins_env" != "/custom-config/enabled_plugins" ]]; then
      log_fail "${dep}: expected RABBITMQ_ENABLED_PLUGINS_FILE=/custom-config/enabled_plugins, got ${plugins_env:-<unset>}"
      return 1
    fi
    probe=$(kubectl get deploy "$dep" -n "$shadow_ns" \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="dependency")].startupProbe.exec.command[*]}')
    if [[ "$probe" != *"trace_on"* || "$probe" != *"firehose_ready"* ]]; then
      log_fail "${dep}: startup probe missing trace_on / firehose_ready check"
      return 1
    fi
    log_success "${dep} Firehose startup probe configured"
  done
}

http_otel_rmq_wait_local_beru() {
  wait_local_beru_rollout "$1" "${2:-120s}"
}

http_otel_rmq_setup_pixie() {
  local repo="$1" shadowtest="$2" shadowtest_ns="$3"
  if [[ -z "${USE_PIXIE:-}" ]]; then
    if kubectl get ns pl >/dev/null 2>&1; then
      USE_PIXIE=1
    else
      USE_PIXIE=0
    fi
  fi
  if [[ "$USE_PIXIE" != "1" ]]; then
    echo "==> MongoDB egress via Pixie: skipped (no pl namespace; set USE_PIXIE=1)"
    HTTP_OTEL_RMQ_MONGO=0
    return 0
  fi
  # shellcheck source=testing/bats/helpers/pixie-bridge.sh
  source "$repo/testing/bats/helpers/pixie-bridge.sh"
  # shellcheck source=testing/bats/helpers/siphon-config.sh
  source "$repo/testing/bats/helpers/siphon-config.sh"
  wait_pixie_vizier_pem 120
  wait_pixie_vizier_healthy 120
  wait_pixie_http_events_ready 180
  wait_pixie_stream_rule "$shadowtest" "$shadowtest_ns" 120 1
  echo "==> Ensuring pixie-gate has current PxL templates"
  kubectl apply -k "$repo/pipeline/pixie-gate/deploy/" >/dev/null
  restart_pixie_gate
  wait_pixie_mongo_pxl_ready "$shadowtest" "$shadowtest_ns" 60

  # Pixie's eBPF probe decodes MongoDB wire protocol only for connections it observes
  # from the initial TCP handshake. Shadow worker pods connect to MongoDB at startup,
  # before Pixie starts watching — restart them so the connections are re-established
  # while the gate is running. The rollout status waits in the caller handle readiness.
  local shadow_ns="shadow-${shadowtest_ns}-${shadowtest}"
  echo "==> Restarting shadow workers so MongoDB connections start under Pixie observation"
  for role in control-a control-b candidate; do
    kubectl rollout restart "deployment/${shadowtest}-${role}" -n "$shadow_ns" 2>/dev/null || true
  done

  echo "==> Waiting 35s for pixie-gate first export cycle"
  sleep 35
}

http_otel_rmq_reverify_pixie() {
  [[ "${USE_PIXIE:-0}" == "1" ]] || return 0
  echo "==> Re-verify Pixie healthy before publish (event must fall in -30s window)"
  wait_pixie_vizier_healthy 120
}

http_otel_rmq_beru_pod_name() {
  local shadow_ns="$1"
  kubectl get pods -n "$shadow_ns" -l app=beru-local \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null \
    || kubectl get pods -n "$shadow_ns" --no-headers 2>/dev/null | awk '/^beru-local-/{print $1; exit}'
}

http_otel_rmq_wait_beru_message() {
  local shadow_ns="$1" label="$2" want_msg="$3" timeout_msg="$4" wait_secs="$5" trace_hex="$6"
  local pixie_pxl="${7:-}"
  local beru_pod logs i
  beru_pod=$(http_otel_rmq_beru_pod_name "$shadow_ns")
  if [[ -z "$beru_pod" ]]; then
    log_fail "Beru pod not found in ${shadow_ns}"
    return 1
  fi
  echo "==> Wait for Beru ${label} (up to ${wait_secs}s): ${want_msg}"
  for i in $(seq 1 "$wait_secs"); do
    if [[ -n "$pixie_pxl" && "${USE_PIXIE:-0}" == "1" && -f "$pixie_pxl" ]] && pixie_vizier_healthy 2>/dev/null; then
      run_pixie_export_once "$pixie_pxl" 2>/dev/null || true
    fi
    beru_pod=$(http_otel_rmq_beru_pod_name "$shadow_ns")
    logs=$(kubectl logs -n "$shadow_ns" "$beru_pod" --tail=500 2>/dev/null || true)
    if grep -qF "$want_msg" <<<"$logs"; then
      log_success "Beru: ${want_msg}"
      return 0
    fi
    if [[ -n "$timeout_msg" ]] && grep -qF "$timeout_msg" <<<"$logs"; then
      log_fail "Beru: ${timeout_msg}"
      kubectl logs -n "$shadow_ns" "$beru_pod" --tail=120 2>&1 \
        | grep -E "${trace_hex}|INGRESS|regression|payload not JSON" >&2 \
        || kubectl logs -n "$shadow_ns" "$beru_pod" --tail=40 >&2 || true
      return 1
    fi
    if [[ "$i" -lt "$wait_secs" ]]; then
      echo "    waiting (${i}/${wait_secs})..." >&2
      sleep 1
    fi
  done
  log_fail "Beru logs missing '${want_msg}' after ${wait_secs}s"
  kubectl logs -n "$shadow_ns" "$beru_pod" --tail=120 2>&1 \
    | grep -E "${trace_hex}|INGRESS|regression|payload not JSON" >&2 \
    || kubectl logs -n "$shadow_ns" "$beru_pod" --tail=40 >&2 || true
  return 1
}

http_otel_rmq_run_test() {
  local shadowtest="$1" shadow_ns="$2" igris_deploy="$3" trace_hex="$4" trace_tp="$5" log_pattern="$6"
  local igris_url="http://${igris_deploy}.${shadow_ns}.svc.cluster.local:8888/publish"

  echo "==> Multicast via Igris (${igris_url}) traceparent=${trace_tp}"
  local write_out
  write_out=$(http_otel_rmq_in_cluster_curl "http-otel-rmq-${RANDOM}" \
    curl -sS -w '__HTTP_CODE__%{http_code}' -o /dev/null \
    -X POST "${igris_url}" \
    -H "Content-Type: application/json" \
    -H "traceparent: ${trace_tp}" \
    -d '{"e2e":"http-otel-rmq"}')
  echo "    curl: $write_out"
  if ! grep -q '__HTTP_CODE__202' <<<"$write_out"; then
    log_fail "Igris POST /publish expected HTTP 202, got: ${write_out:-<empty>}"
    return 1
  fi
  log_success "Igris accepted multicast (HTTP 202)"

  local ingress_msg egress_msg ingress_timeout_msg
  local ingress_wait="${HTTP_OTEL_RMQ_INGRESS_WAIT_SECS:-30}"
  local egress_wait="${HTTP_OTEL_RMQ_EGRESS_WAIT_SECS:-45}"
  if [[ "${HTTP_OTEL_RMQ_MONGO:-1}" == "1" ]]; then
    ingress_wait="${HTTP_OTEL_RMQ_INGRESS_WAIT_SECS:-45}"
  fi
  ingress_msg="No regression for Trace ${trace_hex}"
  egress_msg="No egress regression for Trace ${trace_hex} (rabbitmq)"
  ingress_timeout_msg="Timed out waiting for Trace ${trace_hex} (INGRESS)"

  http_otel_rmq_wait_beru_message "$shadow_ns" "HTTP ingress" "$ingress_msg" "$ingress_timeout_msg" \
    "$ingress_wait" "$trace_hex" || return 1

  echo "==> Wait for shadow apps to publish egress"
  local role pod
  for role in control-a control-b candidate; do
    pod=$(shadow_app_pod_for_role "$shadow_ns" "$shadowtest" "$role")
    if [[ -z "$pod" ]]; then
      log_fail "no shadow app pod for role ${role}"
      return 1
    fi
    local ok=0
    for _ in $(seq 1 30); do
      if kubectl logs -n "$shadow_ns" "$pod" -c app --tail=100 2>/dev/null | grep -q "$log_pattern"; then
        ok=1
        break
      fi
      sleep 2
    done
    if [[ "$ok" != "1" ]]; then
      log_fail "${role} app logs missing '${log_pattern}'"
      kubectl logs -n "$shadow_ns" "$pod" -c app --tail=40 >&2 || true
      return 1
    fi
    if kubectl logs -n "$shadow_ns" "$pod" -c app --tail=100 2>/dev/null | grep -q "${trace_hex}"; then
      log_fail "${role} app logs contain trace hex ${trace_hex} (app must be trace-unaware)"
      return 1
    fi
    log_success "${role} published egress without logging trace id"
  done

  if [[ "${HTTP_OTEL_RMQ_MONGO:-1}" == "1" ]]; then
    echo "==> Wait for shadow apps mongo insert"
    for role in control-a control-b candidate; do
      pod=$(shadow_app_pod_for_role "$shadow_ns" "$shadowtest" "$role")
      local ok=0
      for _ in $(seq 1 30); do
        if kubectl logs -n "$shadow_ns" "$pod" -c app --tail=100 2>/dev/null | grep -q "mongo insert ok"; then
          ok=1
          break
        fi
        sleep 2
      done
      if [[ "$ok" != "1" ]]; then
        log_fail "${role} app logs missing 'mongo insert ok'"
        kubectl logs -n "$shadow_ns" "$pod" -c app --tail=40 >&2 || true
        return 1
      fi
      log_success "${role} mongo insert ok"
    done
    local mongo_egress_msg="No egress regression for Trace ${trace_hex} (mongodb)"
    local mongo_wait="${HTTP_OTEL_RMQ_MONGO_WAIT_SECS:-120}"
    local mongo_pxl=""
    if [[ "${USE_PIXIE:-0}" == "1" ]]; then
      # shellcheck source=testing/bats/helpers/pixie-bridge.sh
      source "${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}/testing/bats/helpers/pixie-bridge.sh"
      mongo_pxl="${PIXIE_BRIDGE_STATE_DIR:-${REPO:-.}/.cache/pixie-bridge}/${SHADOWTEST_NS:-default}-pixie-${shadowtest}-mongo.pxl"
    fi
    http_otel_rmq_wait_beru_message "$shadow_ns" "MongoDB egress" "$mongo_egress_msg" "" \
      "$mongo_wait" "$trace_hex" "$mongo_pxl" || return 1
  fi

  http_otel_rmq_wait_beru_message "$shadow_ns" "RabbitMQ egress" "$egress_msg" "" "$egress_wait" "$trace_hex" || return 1
  return 0
}
