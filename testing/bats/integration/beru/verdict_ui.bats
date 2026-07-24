#!/usr/bin/env bats
# Beru verdict UI scenarios — inject the same histories as
# pipeline/beru/internal/v2/diff/diff_test.go via /api/v1/debug/seed-reports.
#
# Leave the stack up for dashboard inspection:
#   BATS_KEEP=1 SKIP_BUILD=1 SKIP_LOAD=1 \
#     ./testing/bats/run-one.sh integration/beru/verdict_ui.bats
# Then:
#   kubectl -n shadow-default-bats-beru-verdict-ui port-forward svc/beru-local 8080:8080
#   open http://localhost:8080/dashboard/
#
# Rebuild beru:dev first if seed endpoint is missing:
#   make -C pipeline/beru docker-build BERU_IMG=beru:dev && minikube image load beru:dev
#
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/beru-verdict-ui"

setup_file() {
  bats_begin_suite "bats-beru-verdict-ui" "default"

  echo "==> [verdict_ui] ensure_platform_ready" >&3
  ensure_platform_ready

  echo "==> [verdict_ui] apply prod target" >&3
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-integ-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  echo "==> [verdict_ui] apply ShadowTest ${SHADOWTEST}" >&3
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  echo "==> [verdict_ui] wait ShadowTest Ready" >&3
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  bats_source_e2e_helpers
  echo "==> [verdict_ui] wait beru-local in ${SHADOW_NS}" >&3
  wait_local_beru_rollout "$SHADOW_NS"

  # Fail fast if this beru:dev image predates the seed endpoint.
  echo "==> [verdict_ui] probe seed endpoint" >&3
  local probe
  probe=$(beru_http_post "$SHADOW_NS" "/api/v1/debug/seed-reports" '{"reports":[]}' || true)
  if echo "$probe" | grep -qi '404\|Not Found'; then
    echo "seed endpoint missing on beru-local — rebuild/load beru:dev then retry" >&2
    echo "  make -C pipeline/beru docker-build BERU_IMG=beru:dev && minikube image load beru:dev" >&2
    echo "  kubectl -n ${SHADOW_NS} rollout restart deploy/beru-local" >&2
    return 1
  fi

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
  echo "==> [verdict_ui] setup complete (SHADOW_NS=${SHADOW_NS})" >&3
}

setup() {
  isolate_test_state
}

# --- scenarios mirroring diff_test.go ---

@test "UI seed: MATCH clean mongo triple" {
  local tid="$BATS_TRACE_ID" sig="mongodb:insert:orders" payload='{"v":1}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a mongodb egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b mongodb egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate mongodb egress "$sig" "" "$payload")"
  assert_success

  run beru_wait_verdict_settled "$tid" mongodb --expect-status=MATCH --timeout=60
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: VOIDED_BASELINE_DIVERGENCE control status 200 vs 500" {
  local tid="$BATS_TRACE_ID" sig="http:GET:/items" payload='{}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a http ingress "$sig" "200" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b http ingress "$sig" "500" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate http ingress "$sig" "200" "$payload")"
  assert_success

  sleep 2
  run beru_assert_verdict_status "$tid" http VOIDED_BASELINE_DIVERGENCE
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: intentional error path controls 400 candidate 200 -> MISMATCH" {
  local tid="$BATS_TRACE_ID" sig="http:GET:/missing"
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a http ingress "$sig" "400" '{"error":"not found"}')" \
    "$(beru_seed_report_obj "$tid" control-b http ingress "$sig" "400" '{"error":"not found"}')" \
    "$(beru_seed_report_obj "$tid" candidate http ingress "$sig" "200" '{"ok":true}')"
  assert_success

  run beru_wait_verdict_settled "$tid" http --expect-status=MISMATCH --timeout=60
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: compound MISMATCH payload + count (price 10 vs 20 + extra)" {
  local tid="$BATS_TRACE_ID" sig="mongodb:insert:orders"
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a mongodb egress "$sig" "" '{"price":10}')" \
    "$(beru_seed_report_obj "$tid" control-b mongodb egress "$sig" "" '{"price":10}')" \
    "$(beru_seed_report_obj "$tid" candidate mongodb egress "$sig" "" '{"price":20}')" \
    "$(beru_seed_report_obj "$tid" candidate mongodb egress "$sig" "" '{"price":1}')"
  assert_success

  run beru_wait_verdict_settled "$tid" mongodb --expect-status=MISMATCH --expect-count-regression=1 --timeout=60
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: VOIDED_BASELINE_DIVERGENCE control egress count mismatch" {
  local tid="$BATS_TRACE_ID" sig="rabbitmq:publish:events" payload='{"id":1}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" "$payload")"
  assert_success

  sleep 2
  run beru_assert_verdict_status "$tid" rabbitmq VOIDED_BASELINE_DIVERGENCE
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: WAITING_FOR_ROLES incomplete past timeout" {
  local tid="$BATS_TRACE_ID" sig="mongodb:find:x" payload='{}'
  local old
  old=$(date -u -d '30 seconds ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-30S +%Y-%m-%dT%H:%M:%SZ)
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a mongodb egress "$sig" "" "$payload" "$old")" \
    "$(beru_seed_report_obj "$tid" control-b mongodb egress "$sig" "" "$payload" "$old")"
  assert_success

  local i=0 status
  while [[ $i -lt 30 ]]; do
    status=$(beru_http_get "${SHADOW_NS}" "/api/v1/traces/${tid}?protocol=mongodb" \
      | jq -r '.verdict.status // .verdict.Status // empty' 2>/dev/null || true)
    if [[ "$status" == "WAITING_FOR_ROLES" ]]; then
      echo "ok status=WAITING_FOR_ROLES"
      beru_print_ui_hint "$tid"
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  echo "timeout waiting for WAITING_FOR_ROLES (last=${status})" >&2
  return 1
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
