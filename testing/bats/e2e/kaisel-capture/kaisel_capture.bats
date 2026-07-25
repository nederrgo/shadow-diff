#!/usr/bin/env bats
# E2E: Monarch → prod → Kaisel eBPF → igris-http → shadow pods (control-a/b/candidate).
#
# Pipeline under test:
#   1. Monarch lists prod pod IPs into a KaiselRule (+ igrisBaseURL, samplePercentage).
#   2. Kaisel reconciles the CR into live eBPF maps.
#   3. Traced HTTP to those IPs is captured, admitted, and POSTed to igris-http.
#   4. igris multicasts to the three shadow Services; nginx access logs prove delivery.
#
# Requirements:
#   - Cluster with docker/kind/minikube image load
#   - make test-bats-kaisel  (or SKIP_BUILD=1 SKIP_LOAD=1 when images are warm)

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/kaisel-capture"
PROD_DEPLOY="kaisel-capture-prod"

setup_file() {
  bats_begin_suite "bats-kaisel-capture" "default"

  kaisel_setup_platform
  bats_suite_mark MONARCH_DEPLOYED 1

  kaisel_daemonset_deploy
  bats_suite_mark KAISEL_DEPLOYED 1

  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available "deployment/${PROD_DEPLOY}" \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # Full shadow stack Ready (igris, beru-local, shop, recorder, three roles) + KaiselRule.
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel
  SHADOW_NS="$(shadow_namespace)"
  export SHADOW_NS
  bats_suite_mark SHADOW_NS "$SHADOW_NS"

  wait_kaiselrule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"
  bats_suite_mark KAISELRULE_READY 1

  monarch_wait_igris_running "$SHADOW_NS" "$SHADOWTEST" 180
  monarch_wait_all_roles_running "$SHADOW_NS" "$SHADOWTEST" 180

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
    kubectl delete -f "${FIXTURE_DIR}/prod-target.yaml" --ignore-not-found || true
    kaisel_daemonset_teardown || true
  fi
}

@test "monarch writes igrisBaseURL on KaiselRule for Kaisel export" {
  bats_load_suite_state

  local url
  url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.igrisBaseURL}')
  [[ -n "$url" ]] || fail "KaiselRule.spec.igrisBaseURL is empty"
  [[ "$url" == http://*igris* ]] || fail "unexpected igrisBaseURL: ${url}"
}

@test "full route: prod → Kaisel → igris → all three shadow pods" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset — setup did not reach Ready"

  local probe trace_id
  probe="/full-route-${RANDOM}"
  trace_id="$(openssl rand -hex 16)"

  run kaisel_publish_prod_traced "$probe" "$trace_id"
  assert_success
  # helper echoes the trace id on the last line
  trace_id="$(echo "$output" | tail -1)"

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success

  run kaisel_assert_igris_multicast "$trace_id" 90
  assert_success

  run kaisel_assert_shadow_roles_saw_path "$probe"
  assert_success
}

@test "kaisel captures HTTP GET to prod pod after KaiselRule IP sync" {
  bats_load_suite_state

  local prod_ip
  prod_ip=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].status.podIP}')
  [[ -n "$prod_ip" ]] || skip "prod pod has no IP"

  local probe="/kaisel-probe-${RANDOM}"
  kubectl run kaisel-probe --restart=Never --rm -i --image=curlimages/curl:latest \
    -n default -- curl -s --max-time 10 "http://${prod_ip}:80${probe}" || true

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success
}

@test "kaisel captures host and method fields" {
  bats_load_suite_state

  local prod_ip
  prod_ip=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].status.podIP}')
  [[ -n "$prod_ip" ]] || skip "prod pod has no IP"

  kubectl run kaisel-host-probe --restart=Never --rm -i --image=curlimages/curl:latest \
    -n default -- \
    curl -s --max-time 10 -H "Host: prod.example.com" \
    "http://${prod_ip}:80/host-header-test" || true

  sleep 2
  run kaisel_assert_captured 'method=GET'
  assert_success
  run kaisel_assert_captured "host=prod.example.com"
  assert_success
}

@test "kaisel does not capture traffic to an IP not in the KaiselRule" {
  bats_load_suite_state
  # 192.0.2.1 is TEST-NET-1 (RFC 5737); never targeted by any KaiselRule.
  run kaisel_assert_not_captured "dst=192.0.2.1"
  assert_success
}

@test "monarch and kaisel self-heal after the prod pod crashes and is replaced" {
  bats_load_suite_state

  local old_pod old_ip new_ip probe
  old_pod=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].metadata.name}')
  old_ip=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].status.podIP}')
  [[ -n "$old_pod" && -n "$old_ip" ]] || skip "prod pod not found"

  kubectl delete pod "$old_pod" -n default --grace-period=0 --force 2>&1 | tail -3

  new_ip=$(wait_new_pod_ip "app=kaisel-capture-prod" "default" "$old_ip" 90)
  [[ -n "$new_ip" ]] || fail "no replacement pod IP appeared"
  [[ "$new_ip" != "$old_ip" ]]

  wait_kaiselrule_targetip_is "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS" "$new_ip" 60
  sleep 5

  probe="/post-crash-probe-${RANDOM}"
  kubectl run kaisel-crash-probe --restart=Never --rm -i --image=curlimages/curl:latest \
    -n default -- curl -s --max-time 10 "http://${new_ip}:80${probe}" || true

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success
}
