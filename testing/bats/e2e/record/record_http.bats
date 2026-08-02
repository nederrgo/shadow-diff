#!/usr/bin/env bats
# E2E: record mode — Kaisel → Igris/Shop → MinIO (no ABC shadow pods).
#
# Pipeline under test:
#   1. Monarch reconciles mode=record + required storage (MinIO BYOB).
#   2. Stack = KaiselRule + igris + shop (+ beru-local); no control-a/b/candidate.
#   3. Traced prod ingress is POSTed to Igris and flushed as JSONL under sessions/<id>/ingress/.
#   4. Traced prod egress pairs are POSTed to Shop and flushed under sessions/<id>/egress/.
#
# Requirements:
#   - Cluster with docker/kind/minikube image load
#   - make test-bats-record  (or SKIP_BUILD=1 SKIP_LOAD=1 when images are warm)

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/record-http"
PROD_FIXTURE="${BATS_TEST_DIRNAME}/../../fixtures/e2e/kaisel-capture/prod-target.yaml"
PROD_DEPLOY="kaisel-capture-prod"

setup_file() {
  bats_begin_suite "bats-record-http" "default"

  kaisel_setup_platform
  bats_suite_mark MONARCH_DEPLOYED 1

  minio_ensure
  bats_suite_mark MINIO_READY 1

  kaisel_daemonset_deploy
  bats_suite_mark KAISEL_DEPLOYED 1

  kubectl apply -f "${PROD_FIXTURE}"
  kubectl wait --for=condition=Available "deployment/${PROD_DEPLOY}" \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  kubectl wait --for=condition=Available deployment/kaisel-egress-dep \
    -n default --timeout=120s
  bats_suite_mark EGRESS_DEP_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS
  bats_suite_mark SHADOW_NS "$SHADOW_NS"

  wait_kaiselrule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"
  bats_suite_mark KAISELRULE_READY 1

  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST" 180
  kubectl wait --for=condition=Available deployment/shop \
    -n "$SHADOW_NS" --timeout=180s

  kaisel_daemonset_wait_ready 120
  bats_suite_mark KAISEL_READY 1

  # Controller-runtime needs a beat to push KaiselRule IPs into BPF maps.
  sleep 5

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

teardown_file() {
  if [[ "${BATS_KEEP:-0}" != "1" ]]; then
    delete_shadowtest_and_verify "$SHADOWTEST" "$SHADOWTEST_NS" || true
    kubectl delete -f "${PROD_FIXTURE}" --ignore-not-found || true
    kaisel_daemonset_teardown || true
  fi
}

@test "record stack: Ready, KaiselRule URLs, OPERATING_MODE=record, no ABC" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset — setup did not reach Ready"

  local phase mode
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}')
  [[ "$phase" == "Ready" ]] || fail "expected Ready, got ${phase}"

  mode=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.mode}')
  [[ "$mode" == "record" ]] || fail "expected mode=record, got ${mode}"

  local igris_url egress_url
  igris_url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.igrisBaseURL}')
  [[ -n "$igris_url" ]] || fail "KaiselRule.spec.igrisBaseURL is empty"
  [[ "$igris_url" == http://*igris* ]] || fail "unexpected igrisBaseURL: ${igris_url}"

  egress_url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.egressBaseURL}')
  [[ -n "$egress_url" ]] || fail "KaiselRule.spec.egressBaseURL is empty"
  [[ "$egress_url" == http://shop.* ]] || fail "unexpected egressBaseURL: ${egress_url}"

  local op_igris op_shop
  op_igris=$(kubectl get deploy "${SHADOWTEST}-igris" -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPERATING_MODE")].value}')
  op_shop=$(kubectl get deploy shop -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPERATING_MODE")].value}')
  [[ "$op_igris" == "record" ]] || fail "igris OPERATING_MODE=${op_igris}"
  [[ "$op_shop" == "record" ]] || fail "shop OPERATING_MODE=${op_shop}"

  local abc
  abc=$(kubectl get deploy -n "$SHADOW_NS" -o name 2>/dev/null \
    | grep -E "control-a|control-b|candidate" || true)
  [[ -z "$abc" ]] || fail "record mode must not deploy ABC roles: ${abc}"
}

@test "record stack: status.currentSessionID is minted" {
  bats_load_suite_state

  local session
  session=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session" ]] || fail "status.currentSessionID empty"
  [[ "$session" == session-* ]] || fail "unexpected session id: ${session}"
}

@test "record: traced ingress flushes JSONL under sessions/<id>/ingress/" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset"

  local session probe prefix
  session=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session" ]] || fail "currentSessionID empty"

  probe="/record-ingress-${RANDOM}"
  run kaisel_publish_prod_traced "$probe"
  assert_success

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success

  prefix="$(minio_session_prefix "$SHADOWTEST_NS" "$SHADOWTEST" "$session" ingress)"
  run minio_wait_objects "$prefix" 45
  assert_success
}

@test "record: traced egress flushes JSONL under sessions/<id>/egress/" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset"

  local session prefix trace_id
  session=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.currentSessionID}')
  [[ -n "$session" ]] || fail "currentSessionID empty"

  run kaisel_prod_egress_call "/dep/echo"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded \
    "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/echo" 90
  assert_success

  prefix="$(minio_session_prefix "$SHADOWTEST_NS" "$SHADOWTEST" "$session" egress)"
  run minio_wait_objects "$prefix" 45
  assert_success
}
