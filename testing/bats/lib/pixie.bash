# Pixie gate helpers — no stop/restart between tests.
# shellcheck shell=bash

bats_assert_bridge_running() {
  bats_source_pixie_helpers
  if ! pixie_gate_ready; then
    echo "pixie-gate Deployment is not Ready in monarch-system" >&2
    return 1
  fi
  return 0
}

bats_wait_pixie_after_shadowtest() {
  local shadowtest="$1" ns="$2"
  bats_source_pixie_helpers
  wait_pixie_stream_rule "$shadowtest" "$ns" 120 "${3:-0}"
  if [[ "${3:-0}" == "1" ]]; then
    wait_pixie_mongo_pxl_ready "$shadowtest" "$ns" 60
  fi
}

# Banned in tests: restart_pixie_gate (platform infra; avoid per-test churn)
