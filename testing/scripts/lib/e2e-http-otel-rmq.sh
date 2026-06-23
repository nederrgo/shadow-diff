#!/usr/bin/env bash
# Shared helpers for HTTP ingress → RabbitMQ egress OTel E2E scripts.
# shellcheck shell=bash

http_otel_rmq_detect_cluster() {
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

http_otel_rmq_init_cluster() {
  local repo="$1"
  HTTP_OTEL_RMQ_CLUSTER="${HTTP_OTEL_RMQ_CLUSTER:-$(http_otel_rmq_detect_cluster)}"
  if [[ "$HTTP_OTEL_RMQ_CLUSTER" == minikube ]]; then
    # shellcheck source=testing/scripts/lib/cluster-minikube.sh
    source "$repo/testing/scripts/lib/cluster-minikube.sh"
  fi
  echo "==> E2E cluster: ${HTTP_OTEL_RMQ_CLUSTER}"
}

# Build into minikube's docker daemon when using the docker driver (not none).
http_otel_rmq_prepare_docker_build() {
  if [[ "${HTTP_OTEL_RMQ_CLUSTER:-}" == minikube && "${MINIKUBE_DRIVER:-kvm2}" != none ]]; then
    use_minikube_docker_env
  fi
}

http_otel_rmq_load_image() {
  local img="$1"
  [[ "${SKIP_LOAD:-0}" == "1" ]] && return 0
  case "${HTTP_OTEL_RMQ_CLUSTER:-unknown}" in
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
      local cluster="${KIND_CLUSTER:-$(kind get clusters 2>/dev/null | head -1)}"
      [[ -n "$cluster" ]] || { log_fail "no Kind cluster; set KIND_CLUSTER"; exit 1; }
      kind load docker-image "$img" --name "$cluster"
      ;;
    *)
      log_fail "need kind or minikube cluster (detected: ${HTTP_OTEL_RMQ_CLUSTER})"
      exit 1
      ;;
  esac
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

http_otel_rmq_wait_shadowtest() {
  local shadowtest="$1" shadowtest_ns="$2" relay_deploy="$3"
  local shadow_ns="" i phase relay_ok avail
  for i in $(seq 1 60); do
    phase=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    relay_ok=0
    if [[ -n "$shadow_ns" ]] && kubectl get deploy "$relay_deploy" -n "$shadow_ns" >/dev/null 2>&1; then
      avail=$(kubectl get deploy "$relay_deploy" -n "$shadow_ns" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
      [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
    fi
    echo "    phase=${phase:-<none>} shadowNS=${shadow_ns:-<pending>} egress-relay-ready=${relay_ok} (${i}/60)" >&2
    if [[ "$phase" == "Ready" && -n "$shadow_ns" && "$relay_ok" == "1" ]]; then
      echo "$shadow_ns"
      return 0
    fi
    if [[ "$phase" == "Failed" ]]; then
      kubectl get shadowtest "$shadowtest" -n "$shadowtest_ns" -o yaml | tail -30 >&2
      return 1
    fi
    sleep 5
  done
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

  echo "==> Wait for shadow apps to publish egress"
  local role pod dep
  for role in control-a control-b candidate; do
    dep="${shadowtest}-${role}"
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

  echo "==> Wait for Beru ingress + RabbitMQ egress diff"
  local beru_pod ingress_msg egress_msg ingress_timeout_msg
  local wait_secs="${HTTP_OTEL_RMQ_BERU_WAIT_SECS:-45}"
  ingress_msg="No regression for Trace ${trace_hex}"
  egress_msg="No egress regression for Trace ${trace_hex} (rabbitmq)"
  ingress_timeout_msg="Timed out waiting for Trace ${trace_hex} (INGRESS)"
  beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
  if [[ -z "$beru_pod" ]]; then
    log_fail "Beru pod not found in beru-system"
    return 1
  fi
  local logs ingress_ok=0 egress_ok=0 i
  for i in $(seq 1 "$wait_secs"); do
    beru_pod=$(kubectl get pods -n beru-system --no-headers 2>/dev/null | awk '/^beru-/{print $1; exit}')
    logs=$(kubectl logs -n beru-system "$beru_pod" --tail=400 2>/dev/null || true)
    ingress_ok=0
    egress_ok=0
    grep -qF "$ingress_msg" <<<"$logs" && ingress_ok=1
    grep -qF "$egress_msg" <<<"$logs" && egress_ok=1
    if [[ "$ingress_ok" == "1" && "$egress_ok" == "1" ]]; then
      log_success "Beru reported no ingress regression for trace ${trace_hex}"
      log_success "Beru reported no RabbitMQ egress regression for trace ${trace_hex}"
      return 0
    fi
    if grep -qF "$ingress_timeout_msg" <<<"$logs"; then
      log_fail "Beru timed out waiting for ingress ext_proc reports (${ingress_timeout_msg})"
      kubectl logs -n beru-system "$beru_pod" --tail=120 2>&1 | grep -E "${trace_hex}|INGRESS|regression|payload not JSON" >&2 || \
        kubectl logs -n beru-system "$beru_pod" --tail=40 >&2 || true
      return 1
    fi
    if [[ "$i" -lt "$wait_secs" ]]; then
      echo "    waiting (${i}/${wait_secs}) ingress=${ingress_ok} egress=${egress_ok}" >&2
      sleep 1
    fi
  done
  if [[ "$ingress_ok" != "1" ]]; then
    log_fail "Beru logs missing '${ingress_msg}' after ${wait_secs}s (HTTP ingress still uses Envoy ext_proc, not OTel)"
    kubectl logs -n beru-system "$beru_pod" --tail=120 2>&1 | grep -E "${trace_hex}|INGRESS|regression|payload not JSON" >&2 || \
      kubectl logs -n beru-system "$beru_pod" --tail=40 >&2 || true
    return 1
  fi
  log_fail "Beru logs missing '${egress_msg}' after ${wait_secs}s"
  kubectl logs -n beru-system "$beru_pod" --tail=80 >&2 || true
  return 1
}
