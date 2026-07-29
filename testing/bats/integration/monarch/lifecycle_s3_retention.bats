#!/usr/bin/env bats
# Monarch integration: S3 retention on ShadowTest delete.
#
#   Retain — CR delete leaves objects under shadow-diff/<ns>/<name>/
#   Delete — CR delete scrubs that prefix via finalizer shadow-diff.io/s3-cleanup
#
# Seeds a marker object with mc (no Kaisel traffic required).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-lifecycle"
RETAIN_MANIFEST="${FIXTURE_DIR}/shadowtest-s3-retain.yaml"
DELETE_MANIFEST="${FIXTURE_DIR}/shadowtest-s3-delete.yaml"

setup_file() {
  bats_begin_suite "bats-monarch-s3-retention" "default"
  ensure_platform_ready
  minio_ensure

  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-lifecycle-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

setup() {
  isolate_test_state
}

# Apply CR, seed marker under its test prefix. Prints only the prefix on stdout.
# Usage: prefix=$(_s3_retention_seed <manifest> <cr_name>)
_s3_retention_seed() {
  local manifest="$1" name="$2"
  local session prefix marker

  bats_prepare_shadowtest_slot "$name" "$SHADOWTEST_NS" >&2
  apply_shadowtest "$manifest" >&2
  export SHADOWTEST="$name"
  bats_suite_mark SHADOWTEST "$name"
  bats_suite_mark SHADOWTEST_APPLIED 1
  bats_write_suite_state

  wait_shadowtest_ready "$name" "$SHADOWTEST_NS" --require-kaisel >&2
  session=$(kubectl get shadowtest "$name" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session" ]] || {
    echo "currentSessionID empty" >&2
    return 1
  }

  prefix="$(minio_test_prefix "$SHADOWTEST_NS" "$name")"
  marker="${prefix}sessions/${session}/ingress/retention-marker.jsonl"
  minio_put_object "$marker" '{"marker":"retention-bats"}' >&2
  minio_wait_objects "$prefix" 30 >&2

  printf '%s\n' "$prefix"
}

@test "S3 Retain: delete ShadowTest keeps objects under test prefix" {
  local name="bats-monarch-s3-retain" prefix

  prefix="$(_s3_retention_seed "$RETAIN_MANIFEST" "$name")"
  [[ -n "$prefix" ]] || fail "seed failed"

  kubectl delete shadowtest "$name" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$name" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0

  run minio_wait_objects "$prefix" 30
  assert_success
}

@test "S3 Delete: delete ShadowTest scrubs objects under test prefix" {
  local name="bats-monarch-s3-delete" prefix

  prefix="$(_s3_retention_seed "$DELETE_MANIFEST" "$name")"
  [[ -n "$prefix" ]] || fail "seed failed"

  kubectl delete shadowtest "$name" -n "$SHADOWTEST_NS" --wait=false
  monarch_wait_shadowtest_cleaned "$name" "$SHADOWTEST_NS"
  bats_suite_mark SHADOWTEST_APPLIED 0

  run minio_wait_empty "$prefix" 90
  assert_success
}

teardown_file() {
  kubectl delete shadowtest bats-monarch-s3-retain bats-monarch-s3-delete \
    -n default --ignore-not-found --wait=false 2>/dev/null || true
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
