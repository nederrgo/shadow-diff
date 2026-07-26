#!/usr/bin/env bats
# E2E: Node.js hybrid — RMQ ingress + Mongo + HTTP record/replay + dual egress regressions.

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/rabbit-ingress-nodejs"
MANIFEST_DIR="${REPO}/testing/bats/manifests/rabbitmq-otel-e2e"
HTTP_RECORD_HOST="user-service-nodejs.default.internal"

setup_file() {
  bats_begin_suite "bats-nodejs-hybrid" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed

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
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" \
    --require-mongo --require-rmq --require-kaisel

  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  wait_local_beru_rollout "$SHADOW_NS"

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "verify HTTP ingress reaches all shadow roles (nodejs)" {
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_kaisel_egress_seed; then
    skip "Kaisel egress seed not available"
  fi
  for role in control-a control-b candidate; do
    run assert_worker_http_replay "$role" "$BATS_ORDER_ID"
    assert_success
  done
}

@test "verify RabbitMQ egress count regression (nodejs)" {
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_kaisel_egress_seed; then
    skip "Kaisel egress seed not available"
  fi
  for role in control-a control-b candidate; do
    run assert_worker_processed_order "$role" "$BATS_ORDER_ID"
    assert_success
  done

  run beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" rabbitmq)" --timeout=120
  assert_success
}

teardown_file() {
  bats_teardown_suite \
    "${REPO}/testing/bats/manifests/rabbitmq-e2e/prod-rabbitmq.yaml" \
    "${MANIFEST_DIR}/prod-mongo.yaml" \
    "${MANIFEST_DIR}/prod-user-service-nodejs.yaml" \
    "${MANIFEST_DIR}/prod-nodejs-worker.yaml"
}
