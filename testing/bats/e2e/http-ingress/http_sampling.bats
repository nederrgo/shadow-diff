#!/usr/bin/env bats
# E2E proof: HTTP prod sampling at 10% — record→S3→replay; Postgres for keep.
# Golden keep: V=0; golden drop: V=26 (FNV-1a-64 of full 16-byte trace id).

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/http-sampling"
MANIFEST_DIR="${REPO}/testing/bats/manifests/http-otel-rmq-e2e"
PROD_DEPLOY="http-rmq-go-prod"

SAMPLE_KEEP_TID="00000000000000000000000000000087"
SAMPLE_DROP_TID="000000000000000000000000000000f9"

setup_file() {
  bats_begin_suite "bats-http-sampling" "default"

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

@test "record: in-sample HTTP trace flushes to MinIO" {
  run kaisel_ensure_record_mode
  assert_success
  run publish_prod_http "$SAMPLE_KEEP_TID"
  assert_success
  run e2e_assert_session_objects ingress 60
  assert_success
}

@test "replay: in-sample HTTP trace verdict MATCH in Postgres" {
  run e2e_http_record_then_replay "$SAMPLE_KEEP_TID"
  assert_success
  bats_http_otel_firehose_ready
  run beru_wait_verdict_settled "$SAMPLE_KEEP_TID" http --expect-status=MATCH --timeout=120
  assert_success
}

@test "replay: out-of-sample trace does not reach shadow workers" {
  run kaisel_ensure_record_mode
  assert_success
  # Keep ensures MinIO has ingress objects so switch can proceed; drop is sampled out.
  run publish_prod_http "$SAMPLE_KEEP_TID"
  assert_success
  run publish_prod_http "$SAMPLE_DROP_TID"
  assert_success
  run kaisel_switch_to_replay ingress
  assert_success
  sleep 15
  for role in control-a control-b candidate; do
    run assert_worker_trace_absent "$role" "$SAMPLE_DROP_TID"
    assert_success
  done
}

teardown_file() {
  bats_teardown_suite \
    "${MANIFEST_DIR}/prod-rabbitmq.yaml" \
    "${MANIFEST_DIR}/prod-mongodb.yaml" \
    "${MANIFEST_DIR}/prod-target-go.yaml"
}
