#!/usr/bin/env bats
# E2E: Node.js hybrid — RMQ ingress + Mongo + HTTP record/replay + dual egress regressions.

load '../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../fixtures/e2e/rabbit-ingress-nodejs"
MANIFEST_DIR="${REPO}/testing/scripts/manifests/rabbitmq-otel-e2e"

setup_file() {
  bats_begin_suite "bats-nodejs-hybrid" "default"

  ensure_platform_ready
  build_test_images_if_needed
  load_test_images_if_needed

  kubectl apply -f "${REPO}/testing/scripts/manifests/rabbitmq-e2e/prod-rabbitmq.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-mongo.yaml"
  if kubectl get svc user-service -n prod -o jsonpath='{.spec.clusterIP}' 2>/dev/null | grep -qv '^None$'; then
    kubectl delete svc user-service -n prod --ignore-not-found --wait=true
  fi
  bats_wait_namespace_gone prod 180
  kubectl apply -f "${MANIFEST_DIR}/prod-user-service.yaml"
  kubectl apply -f "${MANIFEST_DIR}/prod-nodejs-worker.yaml"

  kubectl wait --for=condition=Available deployment/rmq-prod-broker -n default --timeout=180s
  kubectl wait --for=condition=Available deployment/mongo-prod -n default --timeout=180s
  kubectl wait --for=condition=Available deployment/user-service -n prod --timeout=180s
  kubectl rollout restart deployment/nodejs-prod-worker -n default >/dev/null
  kubectl rollout status deployment/nodejs-prod-worker -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" \
    --require-mongo --require-rmq --require-siphon

  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  wait_local_beru_rollout "$SHADOW_NS"
  bats_wait_pixie_after_shadowtest "$SHADOWTEST" "$SHADOWTEST_NS" 1

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "verify HTTP ingress reaches all shadow roles" {
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_recorder_seed; then
    skip "Pixie HTTP egress / Recorder seed not available"
  fi
  for role in control-a control-b candidate; do
    run assert_worker_http_replay "$role" "$BATS_ORDER_ID"
    assert_success
  done
}

@test "verify Mongo egress diff is clean for isolated trace" {
  if ! kubectl get svc "${SHADOWTEST}-igris" -n "${SHADOW_NS}" >/dev/null 2>&1; then
    skip "RMQ-only hybrid has no igris-http; use integration/mongo_egress.bats"
  fi
  run multicast_igris_write "$BATS_TRACE_ID" '{"data":"mongo-clean"}'
  assert_success
  run beru_wait_log --grep="$(beru_log_no_egress_regression "$BATS_TRACE_ID" mongodb)" --timeout=90
  assert_success
}

@test "verify candidate Mongo count regression in Beru verdict" {
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_recorder_seed; then
    skip "Pixie HTTP egress / Recorder seed not available"
  fi
  for role in control-a control-b candidate; do
    run assert_worker_processed_order "$role" "$BATS_ORDER_ID"
    assert_success
  done

  run beru_wait_log --grep="$(beru_log_egress_count_regression "$BATS_TRACE_ID" mongodb)" --timeout=120
  assert_success

  run beru_verdict_line_api "$BATS_TRACE_ID" mongodb
  assert_success
  assert_output --regexp '^MISMATCH\|1$'
}

@test "verify RabbitMQ egress count regression" {
  publish_rmq_order "$BATS_TRACE_ID" "$BATS_ORDER_ID"
  if ! wait_recorder_seed; then
    skip "Pixie HTTP egress / Recorder seed not available"
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
    "${REPO}/testing/scripts/manifests/rabbitmq-e2e/prod-rabbitmq.yaml" \
    "${MANIFEST_DIR}/prod-mongo.yaml" \
    "${MANIFEST_DIR}/prod-user-service.yaml" \
    "${MANIFEST_DIR}/prod-nodejs-worker.yaml"
}
