#!/usr/bin/env bats
# Monarch integration: live mode switch GC — record↔replay removes the other side.
#
#   record → replay: KaiselRule deleted; ABC roles created; OPERATING_MODE=replay
#   replay → record: ABC deleted; KaiselRule created; OPERATING_MODE=record
#
# Each @test owns apply → patch → assert → delete (no shared Ready stack).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-lifecycle"
SWITCH_MANIFEST="${FIXTURE_DIR}/shadowtest-switch.yaml"

setup_file() {
  bats_begin_suite "bats-monarch-lifecycle-switch" "default"
  ensure_platform_ready
  minio_ensure

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

@test "mode switch record → replay: KaiselRule removed, ABC roles up" {
  apply_shadowtest "$SWITCH_MANIFEST"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS
  bats_suite_mark SHADOW_NS "$SHADOW_NS"

  monarch_assert_no_abc_roles "$SHADOW_NS"
  monarch_wait_kaisel_rule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"

  local session
  session=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session" ]] || fail "currentSessionID empty before switch"

  monarch_patch_mode "$SHADOWTEST" "$SHADOWTEST_NS" replay "$session"

  # Do not --require-kaisel: replay sets kaiselPhase=Disabled.
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  monarch_wait_no_kaisel_rule "$SHADOWTEST" "$SHADOWTEST_NS" 120
  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST" 180
  monarch_wait_operating_mode "$SHADOW_NS" "$SHADOWTEST" replay 180
  monarch_wait_replay_started "$SHADOWTEST" "$SHADOWTEST_NS" 180

  local kaisel
  kaisel=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.kaiselPhase}')
  [[ "$kaisel" == "Disabled" ]] || fail "kaiselPhase=${kaisel}, want Disabled"

  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0
}

@test "mode switch replay → record: ABC removed, KaiselRule up" {
  apply_shadowtest "$SWITCH_MANIFEST"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  # Enter replay before Ready so we assert GC from a full replay stack.
  monarch_patch_mode "$SHADOWTEST" "$SHADOWTEST_NS" replay "session-lifecycle-empty"
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS
  bats_suite_mark SHADOW_NS "$SHADOW_NS"

  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST" 180
  monarch_assert_no_kaisel_rule "$SHADOWTEST" "$SHADOWTEST_NS"
  monarch_wait_operating_mode "$SHADOW_NS" "$SHADOWTEST" replay 180

  monarch_patch_mode "$SHADOWTEST" "$SHADOWTEST_NS" record

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel
  monarch_wait_no_abc_roles "$SHADOW_NS" 120
  monarch_wait_kaisel_rule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"
  monarch_wait_operating_mode "$SHADOW_NS" "$SHADOWTEST" record 180

  local kaisel replay mode exec_id
  mode=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.mode}')
  kaisel=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.kaiselPhase}')
  replay=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.replayState}')
  exec_id=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentReplayExecutionID}')
  [[ "$mode" == "record" ]] || fail "mode=${mode}, want record"
  [[ "$kaisel" == "Ready" ]] || fail "kaiselPhase=${kaisel}, want Ready"
  [[ -z "$replay" ]] || fail "replayState=${replay}, want empty"
  [[ -z "$exec_id" ]] || fail "currentReplayExecutionID=${exec_id}, want empty in record"

  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0
}

@test "unpinned record→replay→record remints session; replay mints execution id" {
  apply_shadowtest "$SWITCH_MANIFEST"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  local session1 session2 exec1
  session1=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session1" ]] || fail "currentSessionID empty after first record"

  # Replay without pinning spec.sessionID — reuse status.currentSessionID.
  monarch_patch_mode "$SHADOWTEST" "$SHADOWTEST_NS" replay "$session1"
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  monarch_wait_replay_started "$SHADOWTEST" "$SHADOWTEST_NS" 180

  exec1=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentReplayExecutionID}')
  [[ "$exec1" == exec-* ]] || fail "currentReplayExecutionID=${exec1}, want exec-*"

  # Back to record without pinning — must mint a fresh S3 session folder.
  # JSON patch: merge patch cannot clear string fields with "".
  kubectl patch shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --type=json \
    -p '[{"op":"replace","path":"/spec/mode","value":"record"},{"op":"remove","path":"/spec/sessionID"}]'
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel

  session2=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session2" ]] || fail "currentSessionID empty after second record"
  [[ "$session2" != "$session1" ]] || fail "session was not reminted: ${session1}"
  [[ "$session2" == sess-* ]] || fail "currentSessionID=${session2}, want sess-*"

  exec1=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentReplayExecutionID}')
  [[ -z "$exec1" ]] || fail "execution id should clear on record, got ${exec1}"

  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
