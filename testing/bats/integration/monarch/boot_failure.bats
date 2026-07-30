#!/usr/bin/env bats
# Monarch integration: boot failure gates — ImagePullBackOff on igris-rabbitmq after
# the prod AMQP shadow queue is *declared unbound* (Phase 1). Binding (Phase 3) never
# runs because sinks never become Ready. Expects Failed autopsy on the CR, teardown
# of KaiselRule + shadow NS + prod queue, sticky Failed (no recreate), and proof that
# standard kubectl apply cannot inject status.phase=Failed.
#
# Testing pyramid layer: integration (no traffic / Beru data path).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-boot-failure"
APPLY_ST="bats-monarch-boot-failure-apply"

setup_file() {
  bats_begin_suite "bats-monarch-boot-failure" "default"
  ensure_platform_ready
  minio_ensure

  echo "==> apply prod target + prod rabbitmq"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl apply -f "${FIXTURE_DIR}/prod-rabbitmq.yaml"
  kubectl wait --for=condition=Available deployment/monarch-boot-fail-target \
    -n default --timeout=120s
  kubectl wait --for=condition=Available deployment/rmq-prod-broker \
    -n default --timeout=180s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # Phase 1 declares the unbound queue before igris-rabbitmq readiness fails
  # (after beru-local Ready). Phase 3 QueueBind never runs.
  monarch_wait_amqp_queue_name "$SHADOWTEST" "$SHADOWTEST_NS" 180

  monarch_wait_shadowtest_failed "$SHADOWTEST" "$SHADOWTEST_NS" 180

  SHADOW_NS="shadow-${SHADOWTEST_NS}-${SHADOWTEST}"
  export SHADOW_NS
  # Finish teardown: NS + KaiselRule must be gone before assertions.
  monarch_wait_no_kaisel_rule "$SHADOWTEST" "$SHADOWTEST_NS" 120
  elapsed=0
  while kubectl get namespace "$SHADOW_NS" >/dev/null 2>&1; do
    if [[ "$elapsed" -ge 120 ]]; then
      echo "FAIL: shadow NS ${SHADOW_NS} still present after Failed cleanup" >&2
      return 1
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
  bats_load_suite_state
}

@test "boot failure: CR Failed with autopsy message and finalizers retained" {
  local phase message finals
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.message}')
  finals=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.finalizers[*]}')

  [[ "$phase" == "Failed" ]] || fail "phase=${phase}, want Failed"
  [[ "$message" == *"igris-rabbitmq"* || "$message" == *"ImagePullBackOff"* ]] || \
    fail "message should mention igris-rabbitmq or ImagePullBackOff, got: ${message}"
  [[ "$finals" == *"shadowtest.finalizers.shadow-diff.io"* ]] || \
    fail "missing shadowtest finalizer: ${finals}"
  [[ "$finals" == *"shadow-diff.io/s3-cleanup"* ]] || \
    fail "missing s3 finalizer: ${finals}"
}

@test "boot failure: KaiselRule and shadow namespace torn down" {
  assert_kubectl_not_found kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS"
  assert_kubectl_not_found namespace "$SHADOW_NS"
}

@test "boot failure: prod AMQP shadow queue deleted" {
  local queue uid
  queue=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
  if [[ -z "$queue" ]]; then
    uid=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
      -o jsonpath='{.metadata.uid}')
    queue="shadow-diff-$(echo "$uid" | tr '[:upper:]' '[:lower:]')"
  fi
  monarch_assert_prod_queue_absent "$queue"
}

@test "boot failure: sticky Failed does not recreate shadow namespace" {
  sleep 10
  assert_kubectl_not_found namespace "$SHADOW_NS"
  local phase
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  [[ "$phase" == "Failed" ]] || fail "phase=${phase}, want sticky Failed"
}

@test "boot failure: standard kubectl apply cannot inject status.phase=Failed" {
  bats_prepare_shadowtest_slot "$APPLY_ST" "$SHADOWTEST_NS"
  kubectl apply -f "${FIXTURE_DIR}/shadowtest-status-inject.yaml"

  local message phase elapsed=0
  message=$(kubectl get shadowtest "$APPLY_ST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.message}' 2>/dev/null || true)
  [[ "$message" != "injected-by-kubectl-apply" ]] || \
    fail "status.message from apply YAML was stored in etcd (want ignored)"

  # Controller should drive Progressing/Ready — never sticky Failed from the YAML.
  while true; do
    phase=$(kubectl get shadowtest "$APPLY_ST" -n "$SHADOWTEST_NS" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    message=$(kubectl get shadowtest "$APPLY_ST" -n "$SHADOWTEST_NS" \
      -o jsonpath='{.status.message}' 2>/dev/null || true)
    [[ "$message" != "injected-by-kubectl-apply" ]] || \
      fail "injected message appeared after reconcile"

    if [[ "$phase" == "Progressing" || "$phase" == "Ready" ]]; then
      echo "    apply-inject CR phase=${phase} (status from YAML ignored)"
      break
    fi
    if [[ "$phase" == "Failed" && "$message" == "injected-by-kubectl-apply" ]]; then
      fail "sticky Failed latched from kubectl apply status"
    fi
    if [[ "$elapsed" -ge 180 ]]; then
      fail "timed out waiting for Progressing/Ready on apply-inject CR (phase=${phase:-<none>} msg=${message})"
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done

  delete_shadowtest_and_verify "$APPLY_ST" "$SHADOWTEST_NS"
}

teardown_file() {
  delete_shadowtest_and_verify "${APPLY_ST}" "${SHADOWTEST_NS:-default}" 2>/dev/null || true
  bats_teardown_suite \
    "${FIXTURE_DIR}/prod-target.yaml" \
    "${FIXTURE_DIR}/prod-rabbitmq.yaml"
}
