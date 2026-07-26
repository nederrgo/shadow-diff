#!/usr/bin/env bats
# E2E proof: RMQ ingress sampling + Kaisel HTTP egress sampling at 10%.
# In-sample: message fans out to shadows AND Shop is seeded for mock replay.
# Out-of-sample: prod still runs; shadows and Shop never see the trace.
# Golden keep: V=0; golden drop: V=26 (FNV-1a-64 of full 16-byte trace id).

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/rmq-sampling-hybrid"
MANIFEST_DIR="${REPO}/testing/bats/manifests/rabbitmq-otel-e2e"
HTTP_RECORD_HOST="user-service-nodejs.default.internal"

# Keep: (0*100) < (10*256); Drop: (26*100) !< 2560
SAMPLE_KEEP_TID="00000000000000000000000000000087"
SAMPLE_DROP_TID="000000000000000000000000000000f9"

setup_file() {
  bats_begin_suite "bats-rmq-sampling-hybrid" "default"

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

@test "RMQ+Shop sampling: in-sample trace seeds Shop and reaches all shadow roles" {
  local oid="keep-${SAMPLE_KEEP_TID:0:8}"
  publish_rmq_order "$SAMPLE_KEEP_TID" "$oid"
  run kaisel_assert_egress_recorded "trace:${SAMPLE_KEEP_TID}:" 120
  assert_success
  for role in control-a control-b candidate; do
    run assert_worker_http_replay "$role" "$oid"
    assert_success
  done
}

@test "RMQ+Shop sampling: out-of-sample trace is not forwarded to shadows" {
  local oid="drop-${SAMPLE_DROP_TID:0:8}"
  publish_rmq_order "$SAMPLE_DROP_TID" "$oid"
  sleep 20
  for role in control-a control-b candidate; do
    run assert_worker_trace_absent "$role" "$SAMPLE_DROP_TID"
    assert_success
    pod=$(bats_shadow_app_pod "$role")
    run bash -c "! kubectl logs -n ${SHADOW_NS} ${pod} -c app --since=5m 2>/dev/null | grep -Fq order_id=${oid}"
    assert_success
  done
}

@test "RMQ+Shop sampling: out-of-sample trace never seeds Shop" {
  local oid="drop-shop-${SAMPLE_DROP_TID:0:8}"
  publish_rmq_order "$SAMPLE_DROP_TID" "$oid"
  # Prod still egresses; Kaisel sampling must block the Shop seed.
  sleep 20
  local logs
  logs=$(kubectl logs -l app=kaisel -n "$KAISEL_NS" --tail=800 2>/dev/null || true)
  if echo "$logs" | grep '"egress recorded"' | grep -Fq "trace:${SAMPLE_DROP_TID}:"; then
    echo "FAIL: Kaisel seeded Shop for out-of-sample trace ${SAMPLE_DROP_TID}" >&2
    return 1
  fi
}

teardown_file() {
  bats_teardown_suite \
    "${REPO}/testing/bats/manifests/rabbitmq-e2e/prod-rabbitmq.yaml" \
    "${MANIFEST_DIR}/prod-mongo.yaml" \
    "${MANIFEST_DIR}/prod-user-service-nodejs.yaml" \
    "${MANIFEST_DIR}/prod-nodejs-worker.yaml"
}
