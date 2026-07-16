#!/usr/bin/env bats
# Monarch integration: HTTP input — verifies that Monarch correctly reconciles a
# ShadowTest with driver: http_request, bringing up both the igris-http multicast
# hub and the siphon OTLP receiver alongside the three shadow app roles.
#
# Testing pyramid layer: integration (no traffic, no Beru, no Pixie data flow).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-http-input"

setup_file() {
  bats_begin_suite "bats-monarch-http-input" "default"
  ensure_platform_ready

  # Prod target must exist before ShadowTest CR so Monarch can read its container
  # ports during the siphon auto-enable port-match check.
  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-integ-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # --require-siphon: wait until both siphon Deployment has AvailableReplicas > 0
  # AND PixieStreamRule is created. No live Pixie needed for this status.
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-siphon

  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST"

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

@test "igris-http: deployment is Available and shadow app pods are Running" {
  monarch_assert_no_crashloop "$SHADOW_NS"
  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST"
  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST"
}

@test "siphon: deployment is Available and PixieStreamRule is created" {
  monarch_wait_siphon_running "$SHADOW_NS"
  kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS"
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
