#!/usr/bin/env bats
# Monarch integration: live dependency update — adding spec.dependencies to a
# Ready ShadowTest creates per-role dep Deployments and rolls shadow app pods
# with MONGO_URL pointed at loopback (shadow-soldier) plus the soldier sidecar.
#
# Testing pyramid layer: integration (real cluster CreateOrPatch + rollout).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-deps-update"

setup_file() {
  bats_begin_suite "bats-monarch-deps-update" "default"
  ensure_platform_ready
  minio_ensure

  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-deps-update-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS"
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS

  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST"

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

@test "dependency update: creates mongodb deps and rolls shadow apps with MONGO_URL" {
  # Baseline: no mongodb dep Deployments yet.
  run kubectl get deployment mongodb-control-a -n "$SHADOW_NS"
  [ "$status" -ne 0 ]

  local uids_before role
  uids_before="$(monarch_shadow_app_pod_uids "$SHADOW_NS" "$SHADOWTEST")"
  echo "==> app pod UIDs before update: ${uids_before}"

  for role in control-a control-b candidate; do
    run monarch_assert_shadow_app_env "$SHADOW_NS" "$SHADOWTEST" "$role" "MONGO_URL" "mongodb-"
    [ "$status" -ne 0 ]
  done

  echo "==> apply ShadowTest with mongodb dependency"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest-with-mongo.yaml"

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-mongo
  monarch_wait_dependency_available "$SHADOW_NS" "mongodb"

  # Wait for Monarch to patch app Deployments (MONGO_URL + soldier) and roll pods.
  local role deploy
  for role in control-a control-b candidate; do
    deploy="${SHADOWTEST}-${role}"
    echo "==> wait ${deploy} MONGO_URL + rollout"
    local elapsed=0
    while true; do
      if monarch_assert_shadow_app_env "$SHADOW_NS" "$SHADOWTEST" "$role" "MONGO_URL" "127.0.0.1" 2>/dev/null; then
        break
      fi
      if [[ "$elapsed" -ge 120 ]]; then
        monarch_assert_shadow_app_env "$SHADOW_NS" "$SHADOWTEST" "$role" "MONGO_URL" "127.0.0.1"
        return 1
      fi
      sleep 5
      elapsed=$((elapsed + 5))
    done
    kubectl rollout status "deployment/${deploy}" -n "$SHADOW_NS" --timeout=180s
  done

  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST"
  monarch_assert_no_crashloop "$SHADOW_NS"

  local uids_after
  uids_after="$(monarch_shadow_app_pod_uids "$SHADOW_NS" "$SHADOWTEST")"
  echo "==> app pod UIDs after update: ${uids_after}"
  [[ "$uids_before" != "$uids_after" ]] || {
    echo "expected shadow app pods to roll after dependency env injection" >&2
    echo "  before: ${uids_before}" >&2
    echo "  after:  ${uids_after}" >&2
    return 1
  }
  [[ "$uids_after" != *"missing"* ]] || {
    echo "missing app pod UID after rollout: ${uids_after}" >&2
    return 1
  }

  for role in control-a control-b candidate; do
    monarch_assert_shadow_app_env "$SHADOW_NS" "$SHADOWTEST" "$role" \
      "MONGO_URL" "127.0.0.1"
    monarch_assert_shadow_has_soldier "$SHADOW_NS" "$SHADOWTEST" "$role"
  done
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
