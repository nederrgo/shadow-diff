#!/usr/bin/env bats
# E2E proof: igris-rabbitmq prod-gate sampling at 10% (shared FNV full-id rule).
# Record captures in-sample AMQP to S3; replay fans out to A/B/C; Postgres for keep.
# Golden keep: V=0; golden drop: V=26 (FNV-1a-64 of full 16-byte trace id).

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/rmq-sampling"
MANIFEST_DIR="${REPO}/testing/bats/manifests/rabbitmq-otel-e2e"

# Keep: (0*100) < (10*256); Drop: (26*100) !< 2560
SAMPLE_KEEP_TID="00000000000000000000000000000087"
SAMPLE_DROP_TID="000000000000000000000000000000f9"

setup_file() {
  bats_begin_suite "bats-rmq-sampling" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed
  minio_ensure

  kubectl apply -f "${REPO}/testing/bats/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-mongo.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-user-service-nodejs.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-nodejs-worker.yaml"
  kubectl delete deployment python-prod-worker -n default --ignore-not-found --wait=false 2>/dev/null || true

  kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
  kubectl wait --for=condition=Available deployment/mongo-prod -n default --timeout=180s
  kubectl wait --for=condition=Available deployment/user-service-nodejs -n default --timeout=180s
  kubectl rollout restart deployment/nodejs-prod-worker -n default >/dev/null
  kubectl rollout status deployment/nodejs-prod-worker -n default --timeout=120s
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

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "record: in-sample RMQ trace flushes to MinIO" {
  local oid="keep-rec-${SAMPLE_KEEP_TID:0:8}"
  run kaisel_ensure_record_mode
  assert_success
  publish_rmq_order "$SAMPLE_KEEP_TID" "$oid"
  sleep 3
  run e2e_assert_session_objects ingress 60
  assert_success
}

@test "replay: in-sample trace reaches all shadow roles" {
  local oid="keep-${SAMPLE_KEEP_TID:0:8}"
  run kaisel_ensure_record_mode
  assert_success
  publish_rmq_order "$SAMPLE_KEEP_TID" "$oid"
  sleep 3
  run kaisel_switch_to_replay ingress
  assert_success
  for role in control-a control-b candidate; do
    run assert_worker_processed_order "$role" "$oid"
    assert_success
  done
}

@test "replay: out-of-sample trace is not forwarded to shadows" {
  local oid="drop-${SAMPLE_DROP_TID:0:8}"
  run kaisel_ensure_record_mode
  assert_success
  publish_rmq_order "$SAMPLE_KEEP_TID" "keep-gate-${SAMPLE_KEEP_TID:0:8}"
  publish_rmq_order "$SAMPLE_DROP_TID" "$oid"
  sleep 3
  run kaisel_switch_to_replay ingress
  assert_success
  for role in control-a control-b candidate; do
    run assert_worker_trace_absent "$role" "$SAMPLE_DROP_TID"
    assert_success
    pod=$(bats_shadow_app_pod "$role")
    run bash -c "! kubectl logs -n ${SHADOW_NS} ${pod} -c app --since=5m 2>/dev/null | grep -Fq order_id=${oid}"
    assert_success
  done
}

teardown_file() {
  bats_teardown_suite \
    "${REPO}/testing/bats/manifests/rabbitmq-e2e/prod-rabbitmq.yaml" \
    "${MANIFEST_DIR}/prod-mongo.yaml" \
    "${MANIFEST_DIR}/prod-user-service-nodejs.yaml" \
    "${MANIFEST_DIR}/prod-nodejs-worker.yaml"
}
