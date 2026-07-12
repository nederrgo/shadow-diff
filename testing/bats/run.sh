#!/usr/bin/env bash
# Run Bats integration and/or E2E suites with crash cleanup.
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
BATS_DIR="${REPO}/testing/bats"
BATS_BIN="${BATS_DIR}/vendor/bats-core/bin/bats"

# shellcheck source=testing/bats/lib/reporter.bash
source "${BATS_DIR}/lib/reporter.bash"

usage() {
  cat <<EOF
Usage: $(basename "$0") [integration|e2e|all]

  integration  Run testing/bats/integration/*.bats
  e2e          Run testing/bats/e2e/http-ingress/*.bats + rabbitmq-ingress/*.bats
  all          Run both (default)

Jest-like output (tap-mocha-reporter) only when BATS_PARALLEL_JOBS=1.
See testing/bats/README.md and docs/infrastructure/bats-parallel-isolation-roadmap.md
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

# ponytail: rolling semaphore — keeps exactly BATS_PARALLEL_JOBS bats processes live; within-file tests always sequential (shared ShadowTest CR)
# Native bats --jobs is NOT used yet (see bats-parallel-isolation-roadmap.md).
run_e2e_suite() {
  local jobs="${BATS_PARALLEL_JOBS:-1}"
  if [[ "$jobs" -le 1 ]]; then
    bats_invoke "${BATS_DIR}/e2e/http-ingress" "${BATS_DIR}/e2e/rabbitmq-ingress"
    return
  fi
  # Multi-process: interleaved TAP — never pipe to tap-mocha-reporter (bats_invoke enforces this).
  local rc=0 p
  local files=("${BATS_DIR}/e2e/http-ingress"/*.bats "${BATS_DIR}/e2e/rabbitmq-ingress"/*.bats)
  local i=0
  local -a pids=()

  while (( i < ${#files[@]} && ${#pids[@]} < jobs )); do
    bats_invoke "${files[$i]}" &
    pids+=($!)
    (( i += 1 ))
  done

  while (( ${#pids[@]} > 0 )); do
    wait -n "${pids[@]}" 2>/dev/null || true
    local new_pids=()
    for p in "${pids[@]}"; do
      if kill -0 "$p" 2>/dev/null; then
        new_pids+=("$p")
      else
        wait "$p" 2>/dev/null || rc=$?
        if (( i < ${#files[@]} )); then
          bats_invoke "${files[$i]}" &
          new_pids+=($!)
          (( i += 1 ))
        fi
      fi
    done
    pids=("${new_pids[@]}")
  done
  return "$rc"
}

main() {
  local suite="${1:-all}"
  need kubectl
  need jq
  [[ -x "$BATS_BIN" ]] || { echo "run: git clone bats-core into testing/bats/vendor/" >&2; exit 1; }

  export REPO
  export BATS_DIR
  export BATS_BIN
  export BATS_STATE_DIR="${REPO}/.cache/shadow-diff-bats"
  mkdir -p "$BATS_STATE_DIR"

  export BATS_PARALLEL_JOBS="${BATS_PARALLEL_JOBS:-1}"
  export BATS_TEST_TIMEOUT="${BATS_TEST_TIMEOUT:-900}"

  case "$suite" in
    integration)
      bats_invoke "${BATS_DIR}/integration"
      ;;
    e2e)
      run_e2e_suite
      ;;
    all|-h|--help)
      [[ "$suite" == "-h" || "$suite" == "--help" ]] && { usage; exit 0; }
      bats_invoke "${BATS_DIR}/integration"
      run_e2e_suite
      ;;
    *)
      echo "unknown suite: $suite" >&2
      usage
      exit 1
      ;;
  esac
}

main "$@"
