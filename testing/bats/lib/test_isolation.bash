# Per-test isolation between @test blocks sharing one suite stack.
# shellcheck shell=bash

isolate_test_state() {
  export BATS_TRACE_ID="$(openssl rand -hex 16)"
  export BATS_SPAN_ID="$(openssl rand -hex 8)"
  export BATS_ORDER_ID="bats-${BATS_TRACE_ID:0:8}"
  export BATS_TRACE_TP="00-${BATS_TRACE_ID}-${BATS_SPAN_ID}-01"

  [[ "${BATS_ISOLATE_MODE:-trace}" == "full" ]] && reset_dependency_state || true
}

beru_local_pod() {
  local shadow_ns="${1:-${SHADOW_NS:-}}"
  kubectl get pods -n "$shadow_ns" -l app=beru-local \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null \
    || kubectl get pods -n "$shadow_ns" --no-headers 2>/dev/null | awk '/^beru-local-/{print $1; exit}'
}

reset_dependency_state() {
  : # scenario-specific; extend when global Mongo/RMQ asserts are added
}
