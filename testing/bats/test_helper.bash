# Common setup for all Bats suites under testing/bats/.
# shellcheck shell=bash

BATS_ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${BATS_ROOT_DIR}/../.." && pwd)"
export REPO
export BATS_LIB_DIR="${BATS_ROOT_DIR}/lib"

if [[ -n "${BATS_TEST_FILENAME:-}" ]]; then
  BATS_TEST_DIRNAME="$(cd "$(dirname "${BATS_TEST_FILENAME}")" && pwd)"
else
  BATS_TEST_DIRNAME="${BATS_ROOT_DIR}"
fi
export BATS_TEST_DIRNAME

load "${REPO}/testing/bats/vendor/bats-support/load.bash"
load "${REPO}/testing/bats/vendor/bats-assert/load.bash"

# shellcheck source=testing/bats/lib/env.bash
source "${BATS_LIB_DIR}/env.bash"
bats_init_env

for _lib in env cluster kubectl_exec platform pixie shadowtest test_isolation traffic beru_assert http_otel_rmq; do
  # shellcheck source=/dev/null
  source "${BATS_LIB_DIR}/${_lib}.bash"
done
unset _lib

setup() {
  isolate_test_state
}

teardown() {
  if [[ "${BATS_TEST_COMPLETED:-}" == "0" ]]; then
    capture_failure_artifacts || true
  fi
}
