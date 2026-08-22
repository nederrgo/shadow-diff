#!/usr/bin/env bats
# Standalone Beru ↔ Postgres verdict suite.
# Seeds the same histories as pipeline/beru/internal/v2/diff/diff_test.go via
# POST /api/v1/debug/seed-reports into a Deployment that is NOT owned by a
# ShadowTest — only Postgres + the Bbolt WAL flusher are under test.
#
# Leave beru + Postgres rows for The System /diffs inspection:
#   BATS_KEEP=1 SKIP_BUILD=1 SKIP_LOAD=1 \
#     ./testing/bats/run-one.sh integration/beru/postgres_verdict.bats
#   Open The System → ShadowDiff → session-bats-beru-postgres-verdict
#   (skips per-test and suite Postgres deletes; you clean up afterward)
#
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/beru-postgres-verdict"
POSTGRES_DIR="${BATS_TEST_DIRNAME}/../../manifests/postgres"
BERU_VERDICT_NAME="bats-beru-postgres-verdict"

setup_file() {
  bats_begin_suite "$BERU_VERDICT_NAME" "default"

  export BERU_NS=monarch-system
  export BERU_SVC=beru-verdict
  export SHADOWTEST="$BERU_VERDICT_NAME"
  bats_suite_mark BERU_NS "$BERU_NS"
  bats_suite_mark BERU_SVC "$BERU_SVC"

  echo "==> [postgres_verdict] ensure_platform_ready" >&3
  ensure_platform_ready
  bats_ensure_dev_image "${BERU_IMG}" "${REPO}/pipeline/beru" BERU_IMG

  echo "==> [postgres_verdict] apply Postgres fixture" >&3
  kubectl apply -f "${POSTGRES_DIR}/deployment.yaml"
  kubectl apply -f "${POSTGRES_DIR}/service.yaml"
  kubectl rollout status deployment/postgres -n monarch-system --timeout=180s

  echo "==> [postgres_verdict] apply standalone beru-verdict" >&3
  sed "s|image: beru:dev|image: ${BERU_IMG}|g" "${FIXTURE_DIR}/beru.yaml" | kubectl apply -f -
  kubectl rollout status deployment/beru-verdict -n monarch-system --timeout=180s
  kubectl wait --for=condition=Available deployment/beru-verdict -n monarch-system --timeout=120s

  echo "==> [postgres_verdict] require Postgres + WAL boot logs" >&3
  local pod logs i=0
  pod=$(kubectl get pods -n monarch-system -l app=beru-verdict \
    -o jsonpath='{.items[0].metadata.name}')
  [[ -n "$pod" ]] || { echo "beru-verdict pod missing" >&2; return 1; }
  while [[ $i -lt 60 ]]; do
    logs=$(kubectl logs -n monarch-system "$pod" --tail=50 2>/dev/null || true)
    if echo "$logs" | grep -Fq "PostgreSQL storage ready" \
      && echo "$logs" | grep -Fq "WAL flusher ready"; then
      break
    fi
    sleep 1
    i=$((i + 1))
  done
  logs=$(kubectl logs -n monarch-system "$pod" --tail=80 2>/dev/null || true)
  echo "$logs" | grep -Fq "PostgreSQL storage ready" || {
    echo "missing 'PostgreSQL storage ready' in beru-verdict logs" >&2
    echo "$logs" >&2
    return 1
  }
  echo "$logs" | grep -Fq "WAL flusher ready" || {
    echo "missing 'WAL flusher ready' in beru-verdict logs (stale ${BERU_IMG}?)" >&2
    echo "  rebuild+load: make -C pipeline/beru docker-build BERU_IMG=${BERU_IMG} && kind load docker-image ${BERU_IMG} --name shadow-diff" >&2
    echo "  then re-run without SKIP_LOAD, or: kubectl -n monarch-system delete pod -l app=beru-verdict" >&2
    echo "$logs" >&2
    return 1
  }

  echo "==> [postgres_verdict] probe seed endpoint" >&3
  beru_probe_seed_endpoint "$BERU_NS" || return 1

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
  echo "==> [postgres_verdict] setup complete (BERU_SVC=${BERU_SVC}.${BERU_NS})" >&3
}

setup() {
  isolate_test_state
}

teardown() {
  # Belt-and-suspenders: poison-pill must not leave Postgres scaled down.
  beru_postgres_scale 1 >/dev/null 2>&1 || true
  if [[ "${BATS_KEEP:-0}" == "1" ]]; then
    # Keep seeded rows so The System /diffs can show this suite's verdicts.
    unset BATS_POISON_RECOVER_TRACE
  else
    beru_cleanup_trace_postgres "${BATS_TRACE_ID:-}" || true
    beru_cleanup_trace_postgres "${BATS_POISON_RECOVER_TRACE:-}" || true
    unset BATS_POISON_RECOVER_TRACE
  fi
  if [[ "${BATS_TEST_COMPLETED:-}" == "0" ]]; then
    capture_failure_artifacts || true
  fi
}

# --- scenarios mirroring diff_test.go ---

@test "Postgres WAL: MATCH clean mongo triple" {
  local tid="$BATS_TRACE_ID" sig="mongodb:insert:orders" payload='{"v":1}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a mongodb egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b mongodb egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate mongodb egress "$sig" "" "$payload")"
  assert_success

  run beru_assert_verdict_status "$tid" mongodb MATCH "" 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: VOIDED_BASELINE_DIVERGENCE control status 200 vs 500" {
  local tid="$BATS_TRACE_ID" sig="http:GET:/items" payload='{}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a http ingress "$sig" "200" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b http ingress "$sig" "500" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate http ingress "$sig" "200" "$payload")"
  assert_success

  run beru_assert_verdict_status "$tid" http VOIDED_BASELINE_DIVERGENCE "" 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: intentional error path controls 400 candidate 200 -> MISMATCH" {
  local tid="$BATS_TRACE_ID" sig="http:GET:/missing"
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a http ingress "$sig" "400" '{"error":"not found"}')" \
    "$(beru_seed_report_obj "$tid" control-b http ingress "$sig" "400" '{"error":"not found"}')" \
    "$(beru_seed_report_obj "$tid" candidate http ingress "$sig" "200" '{"ok":true}')"
  assert_success

  run beru_assert_verdict_status "$tid" http MISMATCH "" 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: compound MISMATCH payload + count (price 10 vs 20 + extra)" {
  local tid="$BATS_TRACE_ID" sig="mongodb:insert:orders"
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a mongodb egress "$sig" "" '{"price":10}')" \
    "$(beru_seed_report_obj "$tid" control-b mongodb egress "$sig" "" '{"price":10}')" \
    "$(beru_seed_report_obj "$tid" candidate mongodb egress "$sig" "" '{"price":20}')" \
    "$(beru_seed_report_obj "$tid" candidate mongodb egress "$sig" "" '{"price":1}')"
  assert_success

  run beru_assert_verdict_status "$tid" mongodb MISMATCH 1 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: MATCH timestamp noise auto-filtered (A/B/C differ on ts)" {
  local tid="$BATS_TRACE_ID" sig="rabbitmq:publish:events"
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" '{"ts":1000,"v":1}')" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" '{"ts":2000,"v":1}')" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" '{"ts":3000,"v":1}')"
  assert_success

  run beru_assert_verdict_status "$tid" rabbitmq MATCH "" 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: MISMATCH missing egress (controls 2 candidate 1)" {
  local tid="$BATS_TRACE_ID" sig="rabbitmq:publish:order.created" payload='{"id":1}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" "$payload")"
  assert_success

  run beru_assert_verdict_status "$tid" rabbitmq MISMATCH 1 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: VOIDED_BASELINE_DIVERGENCE control egress count mismatch" {
  local tid="$BATS_TRACE_ID" sig="rabbitmq:publish:events" payload='{"id":1}'
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" control-b rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" "$payload")" \
    "$(beru_seed_report_obj "$tid" candidate rabbitmq egress "$sig" "" "$payload")"
  assert_success

  run beru_assert_verdict_status "$tid" rabbitmq VOIDED_BASELINE_DIVERGENCE "" 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: WAITING_FOR_ROLES incomplete past timeout" {
  local tid="$BATS_TRACE_ID" sig="mongodb:find:x" payload='{}'
  local old
  old=$(date -u -d '30 seconds ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-30S +%Y-%m-%dT%H:%M:%SZ)
  run beru_seed_reports \
    "$(beru_seed_report_obj "$tid" control-a mongodb egress "$sig" "" "$payload" "$old")" \
    "$(beru_seed_report_obj "$tid" control-b mongodb egress "$sig" "" "$payload" "$old")"
  assert_success

  run beru_assert_verdict_status "$tid" mongodb WAITING_FOR_ROLES "" 30
  assert_success
  beru_print_ui_hint "$tid"
}

@test "Postgres WAL: poison pill dead-letters after 3 flush failures" {
  local poison_tid="$BATS_TRACE_ID"
  local recover_tid sig="mongodb:insert:poison" payload='{"poison":true}'
  local dlq_status=1
  recover_tid="$(openssl rand -hex 16)"
  export BATS_POISON_RECOVER_TRACE="$recover_tid"

  # Scale-to-0 makes flush fail; emptyDir wipe is OK — we remigrate via beru restart.
  echo "==> [poison] scale Postgres to 0" >&3
  beru_postgres_scale 0
  beru_postgres_wait_down 60

  echo "==> [poison] seed single report (WAL-only ingest)" >&3
  run beru_seed_reports \
    "$(beru_seed_report_obj "$poison_tid" control-a mongodb egress "$sig" "" "$payload")"
  if [[ "$status" -ne 0 ]]; then
    beru_postgres_scale 1
    beru_postgres_wait_ready 120 || true
    echo "seed failed while Postgres down: ${output}" >&2
    return 1
  fi

  # Fixture BERU_WAL_FLUSH_TIMEOUT=3s → ~10s; allow 100s if image still uses 30s default.
  echo "==> [poison] wait for DLQ after 3 flush failures" >&3
  run beru_wait_dead_letter "$poison_tid" 100
  dlq_status=$status
  local dlq_out="$output"

  # Fail before restart — restart wipes EmptyDir logs/DLQ and hides the cause.
  if [[ "$dlq_status" -ne 0 ]]; then
    echo "dead-letter wait failed: ${dlq_out}" >&2
    beru_postgres_scale 1
    beru_postgres_wait_ready 120 || true
    return 1
  fi

  echo "==> [poison] restore Postgres + remigrate beru-verdict" >&3
  beru_postgres_scale 1
  beru_postgres_wait_ready 120
  beru_verdict_restart_and_wait

  echo "==> [poison] recover with clean MATCH seed" >&3
  run beru_seed_reports \
    "$(beru_seed_report_obj "$recover_tid" control-a mongodb egress "mongodb:insert:orders" "" '{"v":1}')" \
    "$(beru_seed_report_obj "$recover_tid" control-b mongodb egress "mongodb:insert:orders" "" '{"v":1}')" \
    "$(beru_seed_report_obj "$recover_tid" candidate mongodb egress "mongodb:insert:orders" "" '{"v":1}')"
  assert_success

  run beru_assert_verdict_status "$recover_tid" mongodb MATCH "" 30
  assert_success
  beru_print_ui_hint "$recover_tid"
}

teardown_file() {
  bats_load_suite_state || true
  beru_postgres_scale 1 >/dev/null 2>&1 || true
  beru_postgres_wait_ready 120 >/dev/null 2>&1 || true

  if [[ "${BATS_KEEP:-0}" == "1" ]]; then
    echo ""
    echo "============================================================"
    echo "BATS_KEEP=1 — leaving beru-verdict + Postgres rows for UI"
    echo "  The System → ShadowDiff → session-bats-beru-postgres-verdict"
    echo "  (uncheck \"With diffs only\" only if the session list looks empty)"
    echo "  Cleanup later:"
    echo "    kubectl delete -f ${FIXTURE_DIR}/beru.yaml"
    echo "    # optional: wipe suite rows"
    echo "    # beru_cleanup_shadow_test_postgres bats-beru-postgres-verdict"
    echo "============================================================"
    echo ""
    return 0
  fi

  beru_cleanup_shadow_test_postgres "${SHADOWTEST:-bats-beru-postgres-verdict}" || true
  kubectl delete deployment/beru-verdict service/beru-verdict -n monarch-system --ignore-not-found --wait=false || true
}
