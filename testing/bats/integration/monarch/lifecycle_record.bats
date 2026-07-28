#!/usr/bin/env bats
# Monarch integration: record-mode stack — what must be up (and what must not).
#
# Record: KaiselRule + igris + shop (+ beru-local); no ABC roles.
# Delete-path unit coverage remains in shadowtest_delete_lifecycle_test.go.
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-lifecycle"
SHADOWTEST_MANIFEST="${FIXTURE_DIR}/shadowtest-record.yaml"

setup_file() {
  bats_begin_suite "bats-monarch-lifecycle-record" "default"
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

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST" 180
  kubectl wait --for=condition=Available deployment/shop \
    -n "$SHADOW_NS" --timeout=180s

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

@test "record: ShadowTest Ready with Kaisel Ready and minted session" {
  bats_load_suite_state
  local phase mode kaisel session
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  mode=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.mode}')
  kaisel=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.kaiselPhase}')
  session=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')

  [[ "$phase" == "Ready" ]] || fail "phase=${phase}, want Ready"
  [[ "$mode" == "record" ]] || fail "mode=${mode}, want record"
  [[ "$kaisel" == "Ready" ]] || fail "kaiselPhase=${kaisel}, want Ready"
  [[ -n "$session" ]] || fail "currentSessionID empty"
  [[ "$session" == session-* ]] || fail "unexpected session: ${session}"
}

@test "record: KaiselRule present with igris + shop egress URLs" {
  bats_load_suite_state
  monarch_wait_kaisel_rule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"

  local igris_url egress_url
  igris_url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.igrisBaseURL}')
  egress_url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.egressBaseURL}')
  [[ -n "$igris_url" ]] || fail "igrisBaseURL empty"
  [[ "$egress_url" == http://shop.* ]] || fail "egressBaseURL=${egress_url}"
}

@test "record: igris + shop up with OPERATING_MODE=record; no ABC roles" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset"

  monarch_assert_no_crashloop "$SHADOW_NS"
  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST"
  kubectl wait --for=condition=Available deployment/shop -n "$SHADOW_NS" --timeout=60s
  monarch_assert_operating_mode "$SHADOW_NS" "$SHADOWTEST" record
  monarch_assert_no_abc_roles "$SHADOW_NS"
}

@test "record: delete after Ready tears down CR, shadow ns, and KaiselRule" {
  bats_load_suite_state
  kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$SHADOWTEST" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
