#!/usr/bin/env bats
# Monarch integration: ambiguous ports — verifies that a ShadowTest whose target
# Deployment exposes multiple container ports (none named "http") reaches Failed
# phase with a clear error asking the user to set spec.applicationPort.
#
# Testing pyramid layer: integration (no shadow stack, no traffic, no Beru).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-ambiguous-ports"

setup_file() {
  bats_begin_suite "bats-monarch-ambig-ports" "default"
  ensure_platform_ready
  minio_ensure

  echo "==> apply prod target (ambiguous ports)"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-ambig-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # Monarch should resolve the port ambiguity and set phase=Failed quickly.
  monarch_wait_shadowtest_failed "$SHADOWTEST" "$SHADOWTEST_NS" 20

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

@test "ambiguous ports: ShadowTest reaches Failed with disambiguation error" {
  local phase message
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}' 2>/dev/null)
  message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.message}' 2>/dev/null)

  [[ "$phase" == "Failed" ]] || {
    echo "expected phase=Failed, got phase=${phase}" >&2
    return 1
  }
  [[ "$message" == *"applicationPort"* ]] || {
    echo "expected message to mention 'applicationPort', got: ${message}" >&2
    return 1
  }
  [[ "$message" == *"disambiguate"* ]] || {
    echo "expected message to mention 'disambiguate', got: ${message}" >&2
    return 1
  }
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
