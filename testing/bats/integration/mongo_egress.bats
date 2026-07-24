#!/usr/bin/env bats
# Integration: Mongo egress via Pixie OTLP → beru-local (shared ShadowTest per file).

load '../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../fixtures/integration/mongo-egress"

setup_file() {
  bats_begin_suite "bats-mongo-egress" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed

  deploy_prod_stack "${FIXTURE_DIR}/prod-target.yaml"
  bats_suite_mark PROD_DEPLOYED 1
  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-mongo
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  wait_local_beru_rollout "$SHADOW_NS"
  bats_wait_pixie_after_shadowtest "$SHADOWTEST" "$SHADOWTEST_NS" 1

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "ShadowTest and PixieStreamRule are healthy" {
  run kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}'
  assert_success
  assert_output "Ready"

  run kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" -o jsonpath='{.spec.active}'
  assert_success
  assert_output "true"

  bats_assert_bridge_running
}

@test "Mongo egress MATCH for clean multicast write" {
  run multicast_igris_write "$BATS_TRACE_ID" '{"data":"bats-mongo-clean"}'
  assert_success

  run beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)" --timeout=90
  assert_success
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
