#!/usr/bin/env bash
# Run a single .bats file or one filtered @test.
# Usage:
#   ./testing/bats/run-one.sh e2e/rabbitmq-ingress/python_hybrid.bats
#   ./testing/bats/run-one.sh e2e/rabbitmq-ingress/python_hybrid.bats -f 'RabbitMQ egress'
set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/../.." && pwd)}"
BATS_DIR="${REPO}/testing/bats"
BATS_BIN="${BATS_DIR}/vendor/bats-core/bin/bats"

# shellcheck source=testing/bats/lib/reporter.bash
source "${BATS_DIR}/lib/reporter.bash"

export BATS_PARALLEL_JOBS="${BATS_PARALLEL_JOBS:-1}"
export BATS_TEST_TIMEOUT="${BATS_TEST_TIMEOUT:-900}"
export REPO
export BATS_DIR
export BATS_BIN
export BATS_STATE_DIR="${REPO}/.cache/shadow-diff-bats"

cleanup_on_exit() {
  # shellcheck source=testing/bats/lib/env.bash
  source "${BATS_DIR}/lib/env.bash"
  bats_init_env
  # shellcheck source=testing/bats/lib/shadowtest.bash
  source "${BATS_DIR}/lib/shadowtest.bash"
  run.sh_cleanup_suite || true
}
trap cleanup_on_exit EXIT

usage() {
  cat <<EOF
Usage: $(basename "$0") <path-under-testing/bats/> [bats options...]

Examples:
  $(basename "$0") e2e/rabbitmq-ingress/python_hybrid.bats
  $(basename "$0") e2e/rabbitmq-ingress/python_hybrid.bats -f 'RabbitMQ egress'
  $(basename "$0") integration/mongo_egress.bats -f 'PixieStreamRule'

Jest-like output when BATS_PARALLEL_JOBS=1 (default) on a TTY, or BATS_REPORTER=spec.
EOF
}

[[ $# -ge 1 ]] || { usage >&2; exit 1; }

target="$1"
shift
if [[ "$target" != /* ]]; then
  target="${BATS_DIR}/${target}"
fi
[[ -f "$target" ]] || { echo "not found: $target" >&2; exit 1; }

bats_invoke "$@" "$target"
