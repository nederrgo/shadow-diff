#!/usr/bin/env bats
# E2E: HTTP ingress (igris-http) → OTel → Mongo OTLP + RabbitMQ Firehose egress — Node.js worker.

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/http-otel-rmq-nodejs"
MANIFEST_DIR="${REPO}/testing/bats/manifests/http-otel-rmq-e2e"
RMQ_EGRESS_LOG="${RMQ_EGRESS_LOG:-rmq egress published exchange=egress-events}"

setup_file() {
  bats_begin_suite "bats-http-otel-rmq-nodejs" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed

  kubectl apply -f "${MANIFEST_DIR}/prod-target-nodejs.yaml"
  kubectl wait --for=condition=Available deployment/http-rmq-nodejs-prod -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-mongo --require-rmq-egress

  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  wait_local_beru_rollout "$SHADOW_NS"
  bats_http_otel_firehose_ready
  bats_wait_pixie_after_shadowtest "$SHADOWTEST" "$SHADOWTEST_NS" 1
  bats_http_otel_restart_workers_for_pixie
  bats_http_otel_rollout_stack

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "verify HTTP ingress via igris is clean in Beru" {
  bats_http_otel_reverify_pixie
  run publish_igris_http "$BATS_TRACE_ID"
  assert_success
  run beru_wait_log --grep="$(beru_log_no_regression "$BATS_TRACE_ID")" --timeout=45
  assert_success
}

@test "verify shadow workers publish RMQ egress without logging trace id" {
  bats_http_otel_reverify_pixie
  run publish_igris_http "$BATS_TRACE_ID"
  assert_success
  for role in control-a control-b candidate; do
    run assert_worker_log_grep "$role" "$RMQ_EGRESS_LOG"
    assert_success
    run assert_worker_trace_absent "$role" "$BATS_TRACE_ID"
    assert_success
  done
}

@test "verify Mongo egress is clean for isolated trace" {
  bats_pixie_mongo_enabled || skip "Pixie not available (no pl namespace)"
  bats_http_otel_reverify_pixie
  run publish_igris_http "$BATS_TRACE_ID"
  assert_success
  for role in control-a control-b candidate; do
    run assert_worker_log_grep "$role" "mongo insert ok"
    assert_success
  done
  run beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)" --timeout=120
  assert_success
}

@test "verify RabbitMQ egress is clean for isolated trace" {
  bats_http_otel_reverify_pixie
  run publish_igris_http "$BATS_TRACE_ID"
  assert_success
  run beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" rabbitmq)" --timeout=45
  assert_success
}

teardown_file() {
  bats_teardown_suite "${MANIFEST_DIR}/prod-target-nodejs.yaml"
}
