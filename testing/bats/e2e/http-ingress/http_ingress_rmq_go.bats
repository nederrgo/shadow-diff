#!/usr/bin/env bats
# E2E: HTTP ingress record→S3→replay — Go worker; Postgres verdicts.
#
# Ordering: record @test pins one trace and switches to replay once; replay @tests
# reuse that trace (do not run replay @tests alone with -f).

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/http-otel-rmq-go"
MANIFEST_DIR="${REPO}/testing/bats/manifests/http-otel-rmq-e2e"
PROD_DEPLOY="http-rmq-go-prod"
RMQ_EGRESS_LOG="${RMQ_EGRESS_LOG:-rmq egress published exchange=egress-events}"

setup_file() {
  bats_begin_suite "bats-http-ingress-rmq-go" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed
  minio_ensure

  kubectl apply -f "${MANIFEST_DIR}/prod-rabbitmq.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-mongodb.yaml"
  kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=120s
  kubectl wait --for=condition=Available deployment/mongo-prod -n default --timeout=120s

  kubectl apply -f "${MANIFEST_DIR}/prod-target-go.yaml"
  kubectl wait --for=condition=Available deployment/http-rmq-go-prod -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel

  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  wait_local_beru_rollout "$SHADOW_NS"
  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST" 180
  kubectl wait --for=condition=Available deployment/shop -n "$SHADOW_NS" --timeout=180s
  monarch_wait_operating_mode "$SHADOW_NS" "$SHADOWTEST" record 180
  monarch_wait_no_abc_roles "$SHADOW_NS" 120

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "record: traced HTTP ingress flushes to MinIO (Go)" {
  run kaisel_ensure_record_mode
  assert_success
  run publish_prod_http "$BATS_TRACE_ID"
  assert_success
  run e2e_assert_session_objects ingress 60
  assert_success
  bats_pin_suite_trace "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  run kaisel_switch_to_replay ingress
  assert_success
  bats_http_otel_firehose_ready
}

@test "replay: HTTP ingress verdict MATCH in Postgres (Go)" {
  bats_load_suite_state
  bats_use_suite_trace
  bats_assert_replay_mode
  run beru_wait_verdict_settled "$BATS_TRACE_ID" http --expect-status=MATCH --timeout=120
  assert_success
}

@test "replay: shadow workers publish RMQ egress without logging trace id (Go)" {
  bats_load_suite_state
  bats_use_suite_trace
  bats_assert_replay_mode
  for role in control-a control-b candidate; do
    run assert_worker_log_grep "$role" "$RMQ_EGRESS_LOG"
    assert_success
    run assert_worker_trace_absent "$role" "$BATS_TRACE_ID"
    assert_success
  done
}

@test "replay: RabbitMQ egress verdict MATCH in Postgres (Go)" {
  bats_load_suite_state
  bats_use_suite_trace
  bats_assert_replay_mode
  run beru_wait_verdict_settled "$BATS_TRACE_ID" rabbitmq --expect-status=MATCH --timeout=120
  assert_success
}

teardown_file() {
  bats_teardown_suite "${MANIFEST_DIR}/prod-target-go.yaml"
}
