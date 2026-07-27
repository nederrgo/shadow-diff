#!/usr/bin/env bats
# Monarch + igris-http integration: Spike Guard concurrency load shedding.
# Verifies Monarch computes IGRIS_MAX_CONCURRENCY (shadowRoleReplicas × spec.maxQPSPerPod)
# and igris-http sheds requests over that cap with HTTP 429, before any request
# reaches trace resolution or the shadow multicast targets.
#
# Testing pyramid layer: integration (one ShadowTest, live cluster, no Beru diff-of-diffs).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-spike-guard"
MAX_QPS_PER_POD=5

setup_file() {
  bats_begin_suite "bats-spike-guard" "default"
  ensure_platform_ready

  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-spike-guard-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST"
  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST"

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

@test "igris-http Deployment: IGRIS_MAX_CONCURRENCY reflects maxQPSPerPod" {
  local got
  got=$(kubectl get deploy "${SHADOWTEST}-igris" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="IGRIS_MAX_CONCURRENCY")].value}')
  [[ "$got" == "$MAX_QPS_PER_POD" ]] || {
    echo "IGRIS_MAX_CONCURRENCY=${got}, want ${MAX_QPS_PER_POD}" >&2
    return 1
  }
}

@test "igris-http: a single request under capacity is accepted with 202" {
  run publish_igris_http "$BATS_TRACE_ID" '{"spike":"baseline"}' "/spike"
  assert_success
}

@test "igris-http: requests over IGRIS_MAX_CONCURRENCY are shed with 429" {
  # The gate's occupied window per request is tiny (increment → body read →
  # parse → early response → async pool submit → decrement), so overlap only
  # shows up reliably at a large multiple of the cap — empirically 40x was
  # borderline flaky on a local Minikube VM (~200 req vs cap 5); 60x is
  # comfortably past the observed threshold.
  local burst=$((MAX_QPS_PER_POD * 60))
  run spike_guard_fire_concurrent "$burst" "/spike"
  assert_success

  local codes="$output"
  local count_202 count_429 total
  count_202=$(grep -c '^202$' <<<"$codes" || true)
  count_429=$(grep -c '^429$' <<<"$codes" || true)
  total=$(grep -c '^[0-9]\{3\}$' <<<"$codes" || true)
  echo "burst=${burst} 202=${count_202} 429=${count_429} total=${total}" >&2

  [[ "$total" -eq "$burst" ]] || {
    echo "expected ${burst} status codes, got ${total}: ${codes}" >&2
    return 1
  }
  [[ "$count_202" -ge 1 ]] || { echo "expected at least one 202 (accepted)" >&2; return 1; }
  [[ "$count_429" -ge 1 ]] || { echo "expected at least one 429 (shed) — load shedding did not trigger" >&2; return 1; }
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
