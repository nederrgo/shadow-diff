#!/usr/bin/env bats
# Beru verdict UI scenarios — inject the same histories as
# pipeline/beru/internal/v2/diff/diff_test.go via /api/v1/debug/seed-reports.
#
# Seed-only: history is static, so we assert status directly (no quiescence /
# beru_wait_verdict_settled — that helper is for live-traffic drip).
# setup_file waits for beru-local only (not full ShadowTest Ready).
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

  # Seed-only suite: only beru-local is required. Monarch still reconciles the
  # rest of the stack in the background; we do not wait for ShadowTest Ready.
  SHADOW_NS="shadow-${SHADOWTEST_NS}-${SHADOWTEST}"
  export SHADOW_NS
  bats_source_e2e_helpers
  echo "==> [verdict_ui] wait beru-local in ${SHADOW_NS} (skip full Ready)" >&3
  local i=0
  while [[ $i -lt 120 ]]; do
    if kubectl get deploy/beru-local -n "$SHADOW_NS" >/dev/null 2>&1; then
      break
    fi
    sleep 1
    i=$((i + 1))
  done
  if ! kubectl get deploy/beru-local -n "$SHADOW_NS" >/dev/null 2>&1; then
    echo "timeout waiting for beru-local Deployment in ${SHADOW_NS}" >&2
    return 1
  fi
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

  run beru_assert_verdict_status "$tid" mongodb MATCH
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

  run beru_assert_verdict_status "$tid" http MISMATCH
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

  run beru_assert_verdict_status "$tid" mongodb MISMATCH 1
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: MATCH timestamp noise auto-filtered (A/B/C differ on ts)" {
  local tid="$BATS_TRACE_ID" sig="rabbitmq:publish:events"
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" '{"ts":1000,"v":1}')" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" '{"ts":2000,"v":1}')" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" '{"ts":3000,"v":1}')"
  assert_success

  run beru_assert_verdict_status "$tid" rabbitmq MATCH
  assert_success
  beru_print_ui_hint "$tid"
}

@test "UI seed: MISMATCH missing egress (controls 2 candidate 1)" {
  local tid="$BATS_TRACE_ID" sig="rabbitmq:publish:order.created" payload='{"id":1}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" "$payload")"
  assert_success

  run beru_assert_verdict_status "$tid" rabbitmq MISMATCH 1
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

  # Past-timeout incomplete seeds evaluate to WAITING on ingest (or reaper shortly after).
  run beru_assert_verdict_status "$tid" mongodb WAITING_FOR_ROLES
  assert_success
  beru_print_ui_hint "$tid"
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
