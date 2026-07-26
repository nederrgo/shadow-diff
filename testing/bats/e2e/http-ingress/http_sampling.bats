#!/usr/bin/env bats
# E2E proof: HTTP prod sampling at 10% (Kaisel ingress + egress, shared FNV rule).
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
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-mongo --require-rmq-egress --require-kaisel

  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  wait_local_beru_rollout "$SHADOW_NS"
  bats_http_otel_firehose_ready
  bats_http_otel_rollout_stack

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "HTTP sampling: in-sample trace reaches Beru via Kaisel/igris" {
  run publish_prod_http "$SAMPLE_KEEP_TID"
  assert_success
  run beru_wait_log --grep="$(beru_log_no_regression "$SAMPLE_KEEP_TID")" --timeout=120
  assert_success
}

@test "HTTP sampling: out-of-sample trace does not reach shadow workers" {
  run publish_prod_http "$SAMPLE_DROP_TID"
  assert_success
  sleep 25
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
