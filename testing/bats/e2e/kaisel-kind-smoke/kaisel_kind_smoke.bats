#!/usr/bin/env bats
# Kind Kaisel smoke (Phase 1 Minikube→Kind gate).
#
# Three paths only: in-cluster pod→pod ingress, pod→httpbin egress,
# host→NodePort ingress (Kind extraPortMappings). No port-forward.
#
#   make test-bats-kaisel-kind
#   SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-kaisel-kind   # warm images

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/kaisel-kind-smoke"
PROD_DEPLOY="kaisel-capture-prod"
KIND_HOST_INGRESS="http://127.0.0.1:18080"

setup_file() {
  bats_begin_suite "bats-kaisel-kind-smoke" "default"

  kaisel_setup_platform
  bats_suite_mark MONARCH_DEPLOYED 1

  minio_ensure
  bats_suite_mark MINIO_READY 1

  kaisel_daemonset_deploy
  bats_suite_mark KAISEL_DEPLOYED 1

  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
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

  sleep 5

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

teardown_file() {
  if [[ "${BATS_KEEP:-0}" != "1" ]]; then
    delete_shadowtest_and_verify "$SHADOWTEST" "$SHADOWTEST_NS" || true
    kubectl delete -f "${FIXTURE_DIR}/prod-target.yaml" --ignore-not-found || true
    kaisel_daemonset_teardown || true
  fi
}

@test "kind smoke: kaisel captures in-cluster pod-to-pod ingress" {
  bats_load_suite_state

  local prod_ip probe
  prod_ip=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].status.podIP}')
  [[ -n "$prod_ip" ]] || fail "prod pod has no IP"

  probe="/kind-pod-${RANDOM}"
  kubectl run "kaisel-kind-pod-${RANDOM}" --restart=Never --rm -i \
    --image=curlimages/curl:latest -n default -- \
    curl -s --max-time 10 "http://${prod_ip}:8080${probe}" || true

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success
}

@test "kind smoke: kaisel records outside-cluster egress to httpbin.org" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_scenario "external"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded \
    "trace:${trace_id}:GET:${KAISEL_EGRESS_EXTERNAL_HOST}:/get" 120
  assert_success
}

@test "kind smoke: kaisel captures host NodePort ingress (no port-forward)" {
  bats_load_suite_state

  local probe
  probe="/kind-ext-${RANDOM}"

  if ! curl -sS --max-time 10 "${KIND_HOST_INGRESS}${probe}" >/dev/null; then
    fail "host curl ${KIND_HOST_INGRESS}${probe} failed — recreate Kind with testing/bats/kind/config.yaml (kind delete cluster --name shadow-diff && ensure_kind_ready)"
  fi

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success
}
