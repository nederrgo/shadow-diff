#!/usr/bin/env bash
# Run Bats integration and/or E2E suites with crash cleanup.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
BATS_DIR="${REPO}/testing/bats"
BATS_BIN="${BATS_DIR}/vendor/bats-core/bin/bats"

usage() {
  cat <<EOF
Usage: $(basename "$0") [integration|e2e|all]

  integration  Run testing/bats/integration/*.bats
  e2e          Run testing/bats/e2e/*.bats
  all          Run both (default)
EOF
}

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }
}

cleanup_on_exit() {
  # shellcheck source=testing/bats/lib/env.bash
  source "${BATS_DIR}/lib/env.bash"
  bats_init_env
  # shellcheck source=testing/bats/lib/shadowtest.bash
  source "${BATS_DIR}/lib/shadowtest.bash"
  run.sh_cleanup_suite || true
}
trap cleanup_on_exit EXIT

main() {
  local suite="${1:-all}"
  need kubectl
  need jq
  [[ -x "$BATS_BIN" ]] || { echo "run: git clone bats-core into testing/bats/vendor/" >&2; exit 1; }

  export REPO
  export BATS_STATE_DIR="${REPO}/.cache/shadow-diff-bats"
  mkdir -p "$BATS_STATE_DIR"

  export BATS_PARALLEL_JOBS="${BATS_PARALLEL_JOBS:-1}"
  export BATS_TEST_TIMEOUT="${BATS_TEST_TIMEOUT:-900}"

  case "$suite" in
    integration)
      "$BATS_BIN" "${BATS_DIR}/integration"
      ;;
    e2e)
      "$BATS_BIN" "${BATS_DIR}/e2e"
      ;;
    all|-h|--help)
      [[ "$suite" == "-h" || "$suite" == "--help" ]] && { usage; exit 0; }
      "$BATS_BIN" "${BATS_DIR}/integration" "${BATS_DIR}/e2e"
      ;;
    *)
      echo "unknown suite: $suite" >&2
      usage
      exit 1
      ;;
  esac
}

main "$@"
