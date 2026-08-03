#!/usr/bin/env bats
# E2E: Node.js hybrid — RMQ ingress + Mongo + HTTP; record→S3→replay; Postgres verdicts.

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/rabbit-ingress-nodejs"
MANIFEST_DIR="${REPO}/testing/bats/manifests/rabbitmq-otel-e2e"
HTTP_RECORD_HOST="user-service-nodejs.default.internal"

setup_file() {
  bats_begin_suite "bats-nodejs-hybrid" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed
  minio_ensure

  kubectl apply -f "${REPO}/testing/bats/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-mongo.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-user-service-nodejs.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-nodejs-worker.yaml"

  # ponytail: competing prod workers share the orders queue — only one may run per suite
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

_hybrid_record_then_replay() {
  run kaisel_ensure_record_mode
  assert_success
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_kaisel_egress_seed; then
    skip "Kaisel egress seed not available"
  fi
  run kaisel_switch_to_replay both
  assert_success
}

@test "record: RMQ ingress + HTTP egress flush to MinIO (nodejs)" {
  run kaisel_ensure_record_mode
  assert_success
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_kaisel_egress_seed; then
    skip "Kaisel egress seed not available"
  fi
  run e2e_assert_session_objects both 60
  assert_success
}

@test "replay: HTTP egress reaches all shadow roles (nodejs)" {
  _hybrid_record_then_replay
  for role in control-a control-b candidate; do
    run assert_worker_http_replay "$role" "$BATS_ORDER_ID"
    assert_success
  done
}

@test "replay: RabbitMQ egress count regression in Postgres (nodejs)" {
  _hybrid_record_then_replay
  for role in control-a control-b candidate; do
    run assert_worker_processed_order "$role" "$BATS_ORDER_ID"
    assert_success
  done

  run beru_wait_verdict_settled "$BATS_TRACE_ID" rabbitmq \
    --expect-status=MISMATCH --expect-count-regression=1 --timeout=120
  assert_success
}

@test "replay: MongoDB egress count regression in Postgres (nodejs)" {
  _hybrid_record_then_replay
  for role in control-a control-b candidate; do
    run assert_worker_processed_order "$role" "$BATS_ORDER_ID"
    assert_success
  done
  # candidate unconditionally inserts a second "candidate_n1_loop" document per order.
  run beru_wait_verdict_settled "$BATS_TRACE_ID" mongodb \
    --expect-status=MISMATCH --expect-count-regression=1 --timeout=120
  assert_success
}

teardown_file() {
  bats_teardown_suite \
    "${REPO}/testing/bats/manifests/rabbitmq-e2e/prod-rabbitmq.yaml" \
    "${MANIFEST_DIR}/prod-mongo.yaml" \
    "${MANIFEST_DIR}/prod-user-service-nodejs.yaml" \
    "${MANIFEST_DIR}/prod-nodejs-worker.yaml"
}
