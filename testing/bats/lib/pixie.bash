# Pixie bridge helpers — no stop/restart between tests.
# shellcheck shell=bash

bats_assert_bridge_running() {
  bats_source_pixie_helpers
  local pid
  pid=$(_pixie_bridge_running_pid 2>/dev/null || true)
  [[ -n "$pid" ]] || {
    echo "pixie-stream-bridge is not running" >&2
    return 1
  }
  return 0
}

bats_wait_pixie_after_shadowtest() {
  local shadowtest="$1" ns="$2"
  # shellcheck source=testing/bats/helpers/siphon-config.sh
  source "${REPO}/testing/bats/helpers/siphon-config.sh"
  wait_pixie_stream_rule "$shadowtest" "$ns" 120 "${3:-0}"
  if [[ "${3:-0}" == "1" ]]; then
    wait_pixie_mongo_pxl_ready "$shadowtest" "$ns" 60
  fi
}

# Banned in tests: stop_pixie_stream_bridge, start_pixie_stream_bridge_background 1
