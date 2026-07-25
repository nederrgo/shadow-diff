# Environment defaults for Bats suites. Sourced from test_helper.bash.
# shellcheck shell=bash

bats_init_env() {
  if [[ -z "${REPO:-}" ]]; then
    if [[ -n "${BATS_LIB_DIR:-}" ]]; then
      REPO="$(cd "${BATS_LIB_DIR}/../../.." && pwd)"
    else
      REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
    fi
  fi
  export REPO

  export BATS_STATE_DIR="${BATS_STATE_DIR:-${REPO}/.cache/shadow-diff-bats}"
  export PIXIE_GATE_IMG="${PIXIE_GATE_IMG:-pixie-gate:dev}"

  export MONARCH_IMG="${MONARCH_IMG:-monarch:dev}"
  export BERU_IMG="${BERU_IMG:-beru:dev}"
  export SHOP_IMG="${SHOP_IMG:-shop:dev}"
  export IGRIS_IMG="${IGRIS_IMG:-igris-http:dev}"
  export SIPHON_IMG="${SIPHON_IMG:-siphon:dev}"
  export RECORDER_IMG="${RECORDER_IMG:-recorder:dev}"
  export PYTHON_TEST_WORKER_IMG="${PYTHON_TEST_WORKER_IMG:-python-test-worker:dev}"
  export NODEJS_HYBRID_WORKER_IMG="${NODEJS_HYBRID_WORKER_IMG:-nodejs-hybrid-worker:dev}"
  export HTTP_RMQ_PYTHON_WORKER_IMG="${HTTP_RMQ_PYTHON_WORKER_IMG:-http-rmq-python-worker:dev}"
  export HTTP_RMQ_NODEJS_WORKER_IMG="${HTTP_RMQ_NODEJS_WORKER_IMG:-http-rmq-test-app:dev}"
  export HTTP_RMQ_GO_WORKER_IMG="${HTTP_RMQ_GO_WORKER_IMG:-http-rmq-go-worker:dev}"
  export IGRIS_RABBITMQ_IMG="${IGRIS_RABBITMQ_IMG:-igris-rabbitmq:dev}"
  export EGRESS_RELAY_RABBITMQ_IMG="${EGRESS_RELAY_RABBITMQ_IMG:-egress-relay-rabbitmq:dev}"
  export MONGO_IMAGE="${MONGO_IMAGE:-mongo:4.4}"

  export MINIKUBE_DRIVER="${MINIKUBE_DRIVER:-kvm2}"
  export MINIKUBE_CNI="${MINIKUBE_CNI:-flannel}"
  export SHADOWTEST_NS="${SHADOWTEST_NS:-default}"
  export BERU_QUIESCENCE_SEC="${BERU_QUIESCENCE_SEC:-5}"
  export BATS_ISOLATE_MODE="${BATS_ISOLATE_MODE:-trace}"
  export BATS_WIPE_BERU_EACH_TEST="${BATS_WIPE_BERU_EACH_TEST:-0}"
  export SKIP_BUILD="${SKIP_BUILD:-0}"
  export SKIP_LOAD="${SKIP_LOAD:-0}"
  export SKIP_PLATFORM_BOOTSTRAP="${SKIP_PLATFORM_BOOTSTRAP:-0}"
  # When 1, teardown_file / run.sh EXIT trap leave ShadowTest + beru-local up for UI inspection.
  export BATS_KEEP="${BATS_KEEP:-0}"
}

# Stable ShadowTest name per .bats file (set in setup_file).
bats_shadowtest_name_from_file() {
  local file="${1:-${BATS_TEST_FILENAME:-}}"
  [[ -n "$file" ]] || return 1
  basename "$file" .bats | tr '_' '-'
}

bats_suite_state_file() {
  local file="${1:-${BATS_TEST_FILENAME:-}}"
  local base
  base="$(basename "$file" .bats)"
  echo "${BATS_STATE_DIR}/${base}.suite"
}

bats_write_suite_state() {
  local file="${1:-${BATS_TEST_FILENAME:-}}"
  mkdir -p "${BATS_STATE_DIR}"
  cat >"$(bats_suite_state_file "$file")" <<EOF
SHADOWTEST=${SHADOWTEST:-}
SHADOWTEST_NS=${SHADOWTEST_NS:-default}
SHADOW_NS=${SHADOW_NS:-}
PROD_DEPLOYED=${PROD_DEPLOYED:-0}
SHADOWTEST_APPLIED=${SHADOWTEST_APPLIED:-0}
SETUP_COMPLETE=${SETUP_COMPLETE:-0}
SETUP_EPOCH=$(date +%s)
EOF
}

# Call at the very start of setup_file so teardown can find SHADOWTEST even if setup aborts.
bats_begin_suite() {
  local name="$1" ns="${2:-default}"
  export SHADOWTEST="$name"
  export SHADOWTEST_NS="$ns"
  export SHADOW_NS=""
  export PROD_DEPLOYED=0
  export SHADOWTEST_APPLIED=0
  export SETUP_COMPLETE=0
  bats_write_suite_state
}

bats_suite_mark() {
  local key="$1" val="${2:-1}"
  local f
  f="$(bats_suite_state_file)"
  [[ -f "$f" ]] || return 0
  export "${key}=${val}"
  if grep -q "^${key}=" "$f" 2>/dev/null; then
    sed -i "s/^${key}=.*/${key}=${val}/" "$f"
  else
    echo "${key}=${val}" >>"$f"
  fi
}

bats_load_suite_state() {
  bats_read_suite_state 2>/dev/null || return 1
  export SHADOWTEST SHADOWTEST_NS SHADOW_NS PROD_DEPLOYED SHADOWTEST_APPLIED SETUP_COMPLETE
  return 0
}

bats_read_suite_state() {
  local f
  f="$(bats_suite_state_file)"
  [[ -f "$f" ]] || return 1
  # shellcheck disable=SC1090
  source "$f"
}
