# Shared record→replay cycle helpers for multi-@test E2E suites.
# Record @test pins one trace and switches to replay once; replay @tests reuse it.
# shellcheck shell=bash

# Persist trace (and optional order id) in the per-file suite state file.
# Usage: bats_pin_suite_trace <trace_id_hex32> [order_id]
bats_pin_suite_trace() {
  local trace_id="$1" order_id="${2:-bats-${1:0:8}}"
  [[ -n "$trace_id" ]] || {
    echo "bats_pin_suite_trace: empty trace_id" >&2
    return 1
  }
  bats_suite_mark RECORDED_TRACE_ID "$trace_id"
  bats_suite_mark RECORDED_ORDER_ID "$order_id"
  echo "==> [suite] pinned trace=${trace_id} order=${order_id}"
}

# Load pinned trace into BATS_TRACE_ID / BATS_ORDER_ID for replay @tests.
# Fails fast when the record @test did not run (e.g. -f filter on a replay test).
# Usage: bats_use_suite_trace
bats_use_suite_trace() {
  bats_load_suite_state || {
    echo "bats_use_suite_trace: suite state missing — run the full file (record @test first)" >&2
    return 1
  }
  [[ -n "${RECORDED_TRACE_ID:-}" ]] || {
    echo "bats_use_suite_trace: RECORDED_TRACE_ID unset — run the record @test first" >&2
    return 1
  }
  export BATS_TRACE_ID="$RECORDED_TRACE_ID"
  export BATS_ORDER_ID="${RECORDED_ORDER_ID:-bats-${RECORDED_TRACE_ID:0:8}}"
  export BATS_SPAN_ID="$(openssl rand -hex 8)"
  export BATS_TRACE_TP="00-${BATS_TRACE_ID}-${BATS_SPAN_ID}-01"
}

# Fast guard: CR must already be in replay with replayState=started.
# Usage: bats_assert_replay_mode
bats_assert_replay_mode() {
  local name="${SHADOWTEST:?SHADOWTEST unset}"
  local ns="${SHADOWTEST_NS:-default}"
  local mode replay

  mode=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.spec.mode}')
  replay=$(kubectl get shadowtest "$name" -n "$ns" -o jsonpath='{.status.replayState}')

  [[ "$mode" == "replay" ]] || {
    echo "bats_assert_replay_mode: spec.mode=${mode}, want replay (run record @test first)" >&2
    return 1
  }
  [[ "$replay" == "started" ]] || {
    echo "bats_assert_replay_mode: replayState=${replay:-<empty>}, want started" >&2
    return 1
  }
}
