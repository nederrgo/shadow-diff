#!/usr/bin/env bats
# Monarch integration: replay-mode stack — what must be up (and what must not).
#
# Replay: ABC roles + igris + shop (+ beru-local); KaiselRule deleted;
# status.kaiselPhase=Disabled; status.replayState=started (empty session OK).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-lifecycle"
SHADOWTEST_MANIFEST="${FIXTURE_DIR}/shadowtest-replay.yaml"

setup_file() {
  bats_begin_suite "bats-monarch-lifecycle-replay" "default"
  ensure_platform_ready
  minio_ensure

  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-lifecycle-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "$SHADOWTEST_MANIFEST"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # Do not --require-kaisel: replay sets kaiselPhase=Disabled.
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST" 180
  kubectl wait --for=condition=Available deployment/shop \
    -n "$SHADOW_NS" --timeout=180s
  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST" 180
  monarch_wait_operating_mode "$SHADOW_NS" "$SHADOWTEST" replay 180
  monarch_wait_replay_started "$SHADOWTEST" "$SHADOWTEST_NS" 180

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

@test "replay: ShadowTest Ready with Kaisel Disabled and pinned session" {
  bats_load_suite_state
  local phase mode kaisel session replay
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  mode=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.mode}')
  kaisel=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.kaiselPhase}')
  session=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  replay=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.replayState}')

  [[ "$phase" == "Ready" ]] || fail "phase=${phase}, want Ready"
  [[ "$mode" == "replay" ]] || fail "mode=${mode}, want replay"
  [[ "$kaisel" == "Disabled" ]] || fail "kaiselPhase=${kaisel}, want Disabled"
  [[ "$session" == "session-lifecycle-empty" ]] || fail "session=${session}"
  # setup_file already waited; re-assert so the @test documents the contract.
  [[ "$replay" == "started" ]] || fail "replayState=${replay}, want started"
}

@test "replay: KaiselRule absent" {
  bats_load_suite_state
  monarch_assert_no_kaisel_rule "$SHADOWTEST" "$SHADOWTEST_NS"
}

@test "replay: ABC roles + igris + shop up with OPERATING_MODE=replay" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset"

  monarch_assert_no_crashloop "$SHADOW_NS"
  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST"
  kubectl wait --for=condition=Available deployment/shop -n "$SHADOW_NS" --timeout=60s
  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST"
  monarch_assert_operating_mode "$SHADOW_NS" "$SHADOWTEST" replay
}

@test "replay: delete after Ready tears down CR and shadow ns" {
  bats_load_suite_state
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
