#!/usr/bin/env bats
# Monarch integration: prod AMQP shadow-queue QueueDeclare failure takes the
# same markBootFailed autopsy path as deployment ImagePullBackOff (Failed phase,
# KaiselRule + shadow NS + prod queue torn down, sticky Failed, finalizers kept).
#
# Inject hits the production broker (rmq-prod-broker), not per-role shadow NS brokers.
# QueueBind markBootFailed is covered by unit tests (same autopsy helper).
# Testing pyramid layer: integration (no traffic / Beru data path).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-amqp-queue-failure"
DECLARE_ST="bats-monarch-amqp-qfail-declare"

setup_file() {
  bats_begin_suite "bats-monarch-amqp-qfail" "default"
  ensure_platform_ready
  minio_ensure

  echo "==> apply prod target + prod rabbitmq"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl apply -f "${FIXTURE_DIR}/prod-rabbitmq.yaml"
  kubectl wait --for=condition=Available deployment/amqp-qfail-target \
    -n default --timeout=120s
  kubectl wait --for=condition=Available deployment/rmq-prod-broker \
    -n default --timeout=180s
  bats_suite_mark PROD_DEPLOYED 1
  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
  bats_load_suite_state
}

# Shared assertions after markBootFailed.
_assert_amqp_boot_failed_autopsy() {
  local name="$1" want_substr="$2"
  local phase message finals shadow_ns

  phase=$(kubectl get shadowtest "$name" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  message=$(kubectl get shadowtest "$name" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.message}')
  finals=$(kubectl get shadowtest "$name" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.finalizers[*]}')
  shadow_ns="shadow-${SHADOWTEST_NS}-${name}"

  [[ "$phase" == "Failed" ]] || fail "phase=${phase}, want Failed"
  [[ "$message" == *"${want_substr}"* ]] || \
    fail "message should contain ${want_substr}, got: ${message}"
  [[ "$finals" == *"shadowtest.finalizers.shadow-diff.io"* ]] || \
    fail "missing shadowtest finalizer: ${finals}"
  [[ "$finals" == *"shadow-diff.io/s3-cleanup"* ]] || \
    fail "missing s3 finalizer: ${finals}"

  monarch_wait_no_kaisel_rule "$name" "$SHADOWTEST_NS" 120
  local elapsed=0
  while kubectl get namespace "$shadow_ns" >/dev/null 2>&1; do
    if [[ "$elapsed" -ge 120 ]]; then
      fail "shadow NS ${shadow_ns} still present after Failed cleanup"
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done

  sleep 10
  assert_kubectl_not_found namespace "$shadow_ns"
  phase=$(kubectl get shadowtest "$name" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  [[ "$phase" == "Failed" ]] || fail "phase=${phase}, want sticky Failed"
}

@test "queue declare failure: Failed autopsy and runtime torn down" {
  local uid queue

  # Pause reconciler so we can plant a conflicting queue on the *prod* broker
  # before Monarch's QueueDeclare.
  monarch_scale_controller 0
  bats_prepare_shadowtest_slot "$DECLARE_ST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest-declare.yaml"

  uid=$(kubectl get shadowtest "$DECLARE_ST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.uid}')
  [[ -n "$uid" ]] || fail "ShadowTest UID missing after apply"
  queue="shadow-diff-$(echo "$uid" | tr '[:upper:]' '[:lower:]')"
  monarch_declare_conflicting_prod_queue "$queue"

  monarch_scale_controller 1
  monarch_wait_shadowtest_failed "$DECLARE_ST" "$SHADOWTEST_NS" 180
  _assert_amqp_boot_failed_autopsy "$DECLARE_ST" "queue declare"
  monarch_assert_prod_queue_absent "$queue"

  delete_shadowtest_and_verify "$DECLARE_ST" "$SHADOWTEST_NS"
}

teardown_file() {
  # Always restore controller if a test left it scaled down.
  kubectl scale deployment/monarch-controller-manager -n monarch-system --replicas=1 2>/dev/null || true
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s 2>/dev/null || true

  delete_shadowtest_and_verify "${DECLARE_ST}" "${SHADOWTEST_NS:-default}" 2>/dev/null || true
  bats_teardown_suite \
    "${FIXTURE_DIR}/prod-target.yaml" \
    "${FIXTURE_DIR}/prod-rabbitmq.yaml"
}
