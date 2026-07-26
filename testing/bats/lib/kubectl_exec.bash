# Safe kubectl exec/cp helpers for Bats assertions.
# shellcheck shell=bash

assert_kubectl_not_found() {
  local kind="$1" name="$2" ns_flag="" ns=""
  shift 2
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -n) ns="$2"; ns_flag="-n $ns"; shift 2 ;;
      *) shift ;;
    esac
  done
  if kubectl get "$kind" "$name" $ns_flag >/dev/null 2>&1; then
    echo "expected $kind/$name to be absent" >&2
    return 1
  fi
  return 0
}

kubectl_exec_assert() {
  local ns="$1" pod="$2" container="${3:-}" cmd="$4"
  if [[ -n "$container" ]]; then
    kubectl exec -n "$ns" "$pod" -c "$container" -- sh -c "$cmd"
  else
    kubectl exec -n "$ns" "$pod" -- sh -c "$cmd"
  fi
}

kubectl_cp_from_pod() {
  local ns_pod="$1" remote_path="$2" local_path="$3"
  kubectl cp "${ns_pod}:${remote_path}" "$local_path"
}

capture_failure_artifacts() {
  local out_dir="${BATS_STATE_DIR}/failures"
  mkdir -p "$out_dir"
  local ts; ts="$(date +%Y%m%d-%H%M%S)"
  local prefix="${out_dir}/${ts}-${BATS_TEST_NAME:-test}"
  [[ -n "${SHADOWTEST:-}" ]] && kubectl describe shadowtest "$SHADOWTEST" -n "${SHADOWTEST_NS:-default}" >"${prefix}-shadowtest.txt" 2>&1 || true
  [[ -n "${SHADOW_NS:-}" ]] && kubectl get pods -n "$SHADOW_NS" >"${prefix}-pods.txt" 2>&1 || true
  echo "failure artifacts: ${prefix}-*"
}
