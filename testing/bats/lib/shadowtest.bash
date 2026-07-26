# ShadowTest suite deploy (setup_file) and teardown (teardown_file).
# shellcheck shell=bash

shadow_namespace() {
  if [[ -n "${SHADOW_NS:-}" ]]; then
    echo "$SHADOW_NS"
    return 0
  fi
  kubectl get shadowtest "${SHADOWTEST}" -n "${SHADOWTEST_NS}" \
    -o jsonpath='{.status.shadowNamespace}' 2>/dev/null
}

deploy_prod_stack() {
  local pattern="${1:-}"
  local f
  shopt -s nullglob
  for f in $pattern; do
    echo "==> apply prod: $f"
    kubectl apply -f "$f"
  done
  shopt -u nullglob
}

apply_shadowtest() {
  local manifest="$1"
  echo "==> apply ShadowTest: $manifest"
  kubectl apply -f "$manifest"
}

# Clear a stuck or leftover ShadowTest before setup_file re-applies the CR.
# Prod broker must already be up when an RMQ ShadowTest is mid-delete (Monarch
# deletes the prod shadow queue during finalizer reconciliation).
bats_prepare_shadowtest_slot() {
  local name="$1" ns="${2:-default}"
  bats_source_e2e_helpers

  if ! kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; then
    local orphan_ns="shadow-${ns}-${name}"
    if kubectl get namespace "$orphan_ns" >/dev/null 2>&1; then
      echo "==> remove orphan shadow namespace ${orphan_ns}"
      kubectl delete namespace "$orphan_ns" --wait=false 2>/dev/null || true
      wait_shadow_namespace_gone "$orphan_ns" "${BATS_SHADOWTEST_DELETE_WAIT:-180}" || \
        bats_force_remove_shadowtest "$name" "$ns"
    fi
    return 0
  fi

  local deleting
  deleting=$(kubectl get shadowtest "$name" -n "$ns" \
    -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)
  if [[ -n "$deleting" ]]; then
    echo "==> waiting for in-flight ShadowTest delete ${ns}/${name}"
    wait_shadowtest_gone "$name" "$ns" "${BATS_SHADOWTEST_DELETE_WAIT:-180}" || \
      bats_force_remove_shadowtest "$name" "$ns"
    return 0
  fi

  echo "==> remove existing ShadowTest ${ns}/${name} before re-apply"
  delete_shadowtest_and_verify "$name" "$ns" || bats_force_remove_shadowtest "$name" "$ns"
}

bats_force_remove_shadowtest() {
  local name="$1" ns="${2:-default}"
  bats_source_e2e_helpers
  local shadow_ns
  shadow_ns=$(kubectl get shadowtest "$name" -n "$ns" \
    -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  [[ -n "$shadow_ns" ]] || shadow_ns="shadow-${ns}-${name}"

  echo "==> force-remove stuck ShadowTest ${ns}/${name} (shadow ns ${shadow_ns})" >&2
  kubectl patch shadowtest "$name" -n "$ns" --type=merge \
    -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
  kubectl delete shadowtest "$name" -n "$ns" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl delete namespace "$shadow_ns" --ignore-not-found --wait=false 2>/dev/null || true
  wait_shadow_namespace_gone "$shadow_ns" "${BATS_SHADOWTEST_DELETE_WAIT:-120}" || true
}

wait_shadow_namespace_gone() {
  bats_wait_namespace_gone "$@"
}

bats_wait_namespace_gone() {
  local ns="$1" max_wait="${2:-180}"
  [[ -n "$ns" ]] || return 0
  kubectl get namespace "$ns" >/dev/null 2>&1 || return 0
  local i=0
  while kubectl get namespace "$ns" >/dev/null 2>&1; do
    i=$((i + 2))
    if [[ "$i" -gt "$max_wait" ]]; then
      echo "timed out waiting for namespace ${ns} to delete" >&2
      return 1
    fi
    echo "    waiting for namespace ${ns} (${i}s/${max_wait}s)..."
    sleep 2
  done
}

wait_shadowtest_ready() {
  local name="$1" ns="$2"
  shift 2
  local require_mongo=0 require_rmq=0 require_rmq_egress=0 require_kaisel=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --require-mongo) require_mongo=1; shift ;;
      --require-rmq) require_rmq=1; shift ;;
      --require-rmq-egress) require_rmq_egress=1; shift ;;
      --require-kaisel) require_kaisel=1; shift ;;
      *) shift ;;
    esac
  done

  bats_source_e2e_helpers
  local i phase kaisel message actual_ns relay_ok mongo_ok rabbitmq_ok queue
  local max_loops="${SHADOW_WAIT_LOOPS:-90}"

  for ((i = 1; i <= max_loops; i++)); do
    phase=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    message=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.status.message}' 2>/dev/null || true)
    queue=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
    actual_ns=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    kaisel=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.status.kaiselPhase}' 2>/dev/null || true)
    relay_ok=1 mongo_ok=1 rabbitmq_ok=1

    # Fast-fail: once the shadow namespace exists, check every iteration for
    # pods in terminal bad states (CrashLoop, bad image, OOM). These won't
    # self-heal — waiting the full SHADOW_WAIT_LOOPS timeout only wastes time
    # and hides the real error.
    # Note: ImagePullBackOff can be transient on a cold Minikube (first pull of
    # mongo/rabbitmq); dump details to stdout so bats reporters keep them.
    if [[ -n "$actual_ns" ]]; then
      local bad_pods
      bad_pods=$(kubectl get pods -n "$actual_ns" --no-headers 2>/dev/null \
        | grep -E 'CrashLoopBackOff|OOMKilled|ErrImagePull|ImagePullBackOff|InvalidImageName' \
        || true)
      if [[ -n "$bad_pods" ]]; then
        echo "==> [wait_shadowtest_ready] terminal pod failure in ${actual_ns} — failing fast:"
        echo "    ShadowTest phase=${phase:-<none>} kaisel=${kaisel:-<none>} msg=${message:-<none>}"
        echo "    matched rows:"
        echo "$bad_pods" | sed 's/^/      /'
        echo "    all pods:"
        kubectl get pods -n "$actual_ns" -o wide 2>/dev/null | sed 's/^/      /' || true
        echo "    waiting reasons / images:"
        kubectl get pods -n "$actual_ns" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.containerStatuses[*]}{.name}={.state.waiting.reason}({.image});{end}{"\n"}{end}' \
          2>/dev/null | sed 's/^/      /' || true
        echo "    recent events (Warning):"
        kubectl get events -n "$actual_ns" --field-selector type=Warning \
          --sort-by='.lastTimestamp' 2>/dev/null | tail -20 | sed 's/^/      /' || true
        return 1
      fi
    fi

    if [[ ( "$require_rmq" == "1" || "$require_rmq_egress" == "1" ) && -n "$actual_ns" ]]; then
      relay_ok=0
      if kubectl get deploy "${name}-egress-relay-rabbitmq" -n "$actual_ns" >/dev/null 2>&1; then
        avail=$(kubectl get deploy "${name}-egress-relay-rabbitmq" -n "$actual_ns" \
          -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
        [[ "${avail:-0}" -ge 1 ]] && relay_ok=1
      fi
      rabbitmq_ok=0
      if kubectl get deploy rabbitmq-control-a -n "$actual_ns" >/dev/null 2>&1; then
        avail=$(kubectl get deploy rabbitmq-control-a -n "$actual_ns" \
          -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
        [[ "${avail:-0}" -ge 1 ]] && rabbitmq_ok=1
      fi
    fi

    if [[ "$require_mongo" == "1" && -n "$actual_ns" ]]; then
      mongo_ok=0
      if kubectl get deploy mongodb-control-a -n "$actual_ns" >/dev/null 2>&1; then
        avail=$(kubectl get deploy mongodb-control-a -n "$actual_ns" \
          -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
        [[ "${avail:-0}" -ge 1 ]] && mongo_ok=1
      fi
    fi

    local ready=0
    if [[ "$phase" == "Ready" && -n "$actual_ns" ]]; then
      ready=1
      [[ "$require_kaisel" == "1" && "$kaisel" != "Ready" ]] && ready=0
      [[ "$require_rmq" == "1" && ( -z "$queue" || "$relay_ok" != "1" || "$rabbitmq_ok" != "1" ) ]] && ready=0
      [[ "$require_rmq_egress" == "1" && ( "$relay_ok" != "1" || "$rabbitmq_ok" != "1" ) ]] && ready=0
      [[ "$require_mongo" == "1" && "$mongo_ok" != "1" ]] && ready=0
    fi

    if [[ "$ready" == "1" ]]; then
      SHADOW_NS="$actual_ns"
      export SHADOW_NS
      echo "    ShadowTest Ready ns=${SHADOW_NS}"
      return 0
    fi

    if [[ "$phase" == "Failed" ]]; then
      echo "ShadowTest Failed: ${message}" >&2
      return 1
    fi

    echo "    wait Ready (${i}/${max_loops}) phase=${phase:-<none>} kaisel=${kaisel:-<none>} relay=${relay_ok} mongo=${mongo_ok} queue=${queue:-<none>}"
    sleep 5
  done

  echo "timed out waiting for ShadowTest ${ns}/${name} Ready" >&2
  return 1
}

deploy_shared_environment() {
  local fixture_dir="$1"
  deploy_prod_stack "${fixture_dir}/prod"*.yaml "${fixture_dir}/prod-"*.yaml 2>/dev/null || \
    deploy_prod_stack "${fixture_dir}"/*.yaml
  apply_shadowtest "${fixture_dir}/shadowtest.yaml"
}

delete_shadowtest_and_verify() {
  local name="$1" ns="${2:-default}"
  if [[ -z "$name" ]]; then
    echo "==> skip ShadowTest delete (no SHADOWTEST name — setup_file did not reach deploy)" >&2
    return 0
  fi
  bats_source_e2e_helpers
  if ! kubectl get shadowtest "$name" -n "$ns" >/dev/null 2>&1; then
    echo "==> ShadowTest ${ns}/${name} not present (skip delete)" >&2
    return 0
  fi
  SHADOWTEST="$name" SHADOWTEST_NS="$ns" \
    "${REPO}/testing/bats/setup/delete-shadowtest.sh" "$name" "$ns" || return 1
  assert_kubectl_not_found shadowtest "$name" -n "$ns" || return 1
  assert_kubectl_not_found kaiselrule "kaisel-${name}" -n "$ns" || return 1
  local shadow_ns="shadow-${ns}-${name}"
  assert_kubectl_not_found namespace "$shadow_ns" || return 1
}

# Safe teardown_file helper: only removes resources recorded in .suite state.
# ShadowTest is deleted before prod so Monarch can reach the prod RMQ broker for
# queue cleanup during finalizer reconciliation.
bats_teardown_suite() {
  bats_load_suite_state || return 0

  if [[ "${BATS_KEEP:-0}" == "1" ]]; then
    echo ""
    echo "============================================================"
    echo "BATS_KEEP=1 — leaving ShadowTest / beru-local running for UI"
    echo "  ShadowTest:  ${SHADOWTEST_NS:-default}/${SHADOWTEST}"
    echo "  Shadow ns:   ${SHADOW_NS:-shadow-${SHADOWTEST_NS:-default}-${SHADOWTEST}}"
    echo "  Port-forward dashboard:"
    echo "    kubectl -n ${SHADOW_NS:-shadow-${SHADOWTEST_NS:-default}-${SHADOWTEST}} port-forward svc/beru-local 8080:8080"
    echo "  Then open http://localhost:8080/dashboard/"
    echo "  Cleanup later:"
    echo "    kubectl delete shadowtest ${SHADOWTEST} -n ${SHADOWTEST_NS:-default}"
    echo "============================================================"
    echo ""
    return 0
  fi

  if [[ "${SHADOWTEST_APPLIED:-0}" == "1" || "${SETUP_COMPLETE:-0}" == "1" ]]; then
    delete_shadowtest_and_verify "${SHADOWTEST}" "${SHADOWTEST_NS:-default}" || true
  elif kubectl get shadowtest "${SHADOWTEST}" -n "${SHADOWTEST_NS:-default}" >/dev/null 2>&1; then
    delete_shadowtest_and_verify "${SHADOWTEST}" "${SHADOWTEST_NS:-default}" || true
  fi

  if [[ "${PROD_DEPLOYED:-0}" == "1" ]]; then
    while [[ $# -gt 0 ]]; do
      kubectl delete -f "$1" --ignore-not-found --wait=false 2>/dev/null || true
      shift
    done
  fi
}

undeploy_prod_stack() {
  local pattern="${1:-}"
  local f
  shopt -s nullglob
  for f in $pattern; do
    kubectl delete -f "$f" --ignore-not-found --wait=false 2>/dev/null || true
  done
  shopt -u nullglob
}

run.sh_cleanup_suite() {
  bats_read_suite_state 2>/dev/null || return 0
  [[ -n "${SHADOWTEST:-}" ]] || return 0
  if [[ "${BATS_KEEP:-0}" == "1" ]]; then
    echo "==> [bats run.sh] BATS_KEEP=1 — skip stale ShadowTest cleanup for ${SHADOWTEST}"
    return 0
  fi
  if kubectl get shadowtest "$SHADOWTEST" -n "${SHADOWTEST_NS:-default}" >/dev/null 2>&1; then
    echo "==> [bats run.sh] cleanup stale ShadowTest ${SHADOWTEST}"
    delete_shadowtest_and_verify "$SHADOWTEST" "${SHADOWTEST_NS:-default}" || true
  fi
}
