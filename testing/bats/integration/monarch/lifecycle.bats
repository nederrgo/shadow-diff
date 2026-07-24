#!/usr/bin/env bats
# Monarch integration: ShadowTest lifecycle — delete mid-bring-up, re-apply while
# deleting, recreate after clean, and delete after Ready.
#
# Pyramid: integration (real cluster NS GC + API admission). Delete-path branch
# logic lives in unit tests (shadowtest_delete_lifecycle_test.go).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-lifecycle"

setup_file() {
  bats_begin_suite "bats-monarch-lifecycle" "default"
  ensure_platform_ready

  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-lifecycle-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
}

@test "delete mid-bring-up: CR, shadow namespace, and PixieStreamRule are cleaned" {
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  monarch_wait_shadowtest_bringup_started "$SHADOWTEST" "$SHADOWTEST_NS"

  echo "==> delete ShadowTest mid-bring-up"
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false

  local deleting
  deleting=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)
  [[ -n "$deleting" ]] || {
    echo "expected deletionTimestamp after delete --wait=false" >&2
    return 1
  }

  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
}

@test "re-apply while deleting: same UID stays terminating until cleanup finishes" {
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  monarch_wait_shadowtest_bringup_started "$SHADOWTEST" "$SHADOWTEST_NS"

  local uid_before
  uid_before=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.uid}')

  echo "==> delete ShadowTest then immediately re-apply"
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"

  local deleting uid_after
  deleting=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null || true)
  uid_after=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.metadata.uid}' 2>/dev/null || true)

  [[ -n "$deleting" ]] || {
    echo "expected deletionTimestamp to remain after re-apply during delete" >&2
    return 1
  }
  [[ "$uid_before" == "$uid_after" ]] || {
    echo "UID changed during delete (${uid_before} -> ${uid_after}); new life started too early" >&2
    return 1
  }

  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
}

@test "recreate after clean: second life reaches Ready" {
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  monarch_wait_shadowtest_bringup_started "$SHADOWTEST" "$SHADOWTEST_NS"
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"

  echo "==> re-create ShadowTest after clean delete"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"

  local phase shadow_ns
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.shadowNamespace}')

  [[ "$phase" == "Ready" ]] || {
    echo "phase=${phase}, want Ready" >&2
    return 1
  }
  [[ "$shadow_ns" == "shadow-${SHADOWTEST_NS}-${SHADOWTEST}" ]] || {
    echo "shadowNamespace=${shadow_ns}, want shadow-${SHADOWTEST_NS}-${SHADOWTEST}" >&2
    return 1
  }

  monarch_wait_igris_running "$shadow_ns" "$SHADOWTEST"
  monarch_assert_no_crashloop "$shadow_ns"

  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
}

@test "delete after Ready: full stack tears down cleanly" {
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  local shadow_ns
  shadow_ns="$(shadow_namespace)"
  monarch_wait_igris_running "$shadow_ns" "$SHADOWTEST"

  echo "==> delete Ready ShadowTest"
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
