#!/usr/bin/env bats
# E2E: ShadowTest → Monarch reconciler → KaiselRule CR → kaisel DaemonSet eBPF capture → HTTP captured.
#
# Full pipeline:
#   1. Monarch lists prod pod IPs and writes them into a KaiselRule CR.
#   2. kaisel's controller-runtime reconciler watches the CR and pushes IPs into
#      live eBPF hash maps (no daemon restart needed).
#   3. Traffic to those IPs captured off the wire via AF_PACKET on the node's eth0.
#   4. Assertions via kubectl logs on the DaemonSet pods.
#
# Requirements:
#   - Cluster with Monarch deployed (CRDs installed, operator running).
#   - kaisel container image pre-loaded: make -C pipeline/kaisel docker-build
#     then kind load docker-image kaisel:latest  (or equivalent for your registry).
#   - pipeline/kaisel/deploy/ kustomization applied by setup_file below.

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/e2e/kaisel-capture"
PROD_DEPLOY="kaisel-capture-prod"

setup_file() {
  bats_begin_suite "bats-kaisel-capture" "default"

  # Self-contained: build images, install CRDs, deploy Monarch operator, deploy
  # kaisel DaemonSet.  No external platform bootstrap required.
  kaisel_setup_platform
  bats_suite_mark MONARCH_DEPLOYED 1

  # Deploy the kaisel DaemonSet (image already loaded by kaisel_setup_platform).
  kaisel_daemonset_deploy
  bats_suite_mark KAISEL_DEPLOYED 1

  # Deploy the prod HTTP target.
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available "deployment/${PROD_DEPLOY}" \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  # Create the ShadowTest; Monarch reconciles it and emits a KaiselRule.
  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # Wait for Monarch to write the running pod IPs into the KaiselRule.
  wait_kaiselrule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"
  bats_suite_mark KAISELRULE_READY 1

  # Wait for kaisel DaemonSet pods to be running.
  kaisel_daemonset_wait_ready 120
  bats_suite_mark KAISEL_READY 1

  # Give the controller-runtime manager a moment to reconcile the KaiselRule
  # and push the pod IPs into the BPF map.
  sleep 5

  bats_suite_mark SETUP_COMPLETE 1
  bats_write_suite_state
}

@test "monarch writes igrisBaseURL on KaiselRule for Kaisel export" {
  bats_load_suite_state

  local url
  url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.igrisBaseURL}')
  [[ -n "$url" ]] || fail "KaiselRule.spec.igrisBaseURL is empty"
  [[ "$url" == http://*igris* ]] || fail "unexpected igrisBaseURL: ${url}"
}

teardown_file() {
  if [[ "${BATS_KEEP:-0}" != "1" ]]; then
    delete_shadowtest_and_verify "$SHADOWTEST" "$SHADOWTEST_NS" || true
    kubectl delete -f "${FIXTURE_DIR}/prod-target.yaml" --ignore-not-found || true
    kaisel_daemonset_teardown || true
    # Leave Monarch deployed — uninstalling CRDs mid-session breaks any
    # other suites sharing the cluster.  Run `make undeploy uninstall` manually
    # to fully clean up.
  fi
}

@test "kaisel captures HTTP GET to prod pod after KaiselRule IP sync" {
  bats_load_suite_state

  local prod_ip
  prod_ip=$(kubectl get pod -l app=kaisel-capture-prod -n default \
    -o jsonpath='{.items[0].status.podIP}')
  [[ -n "$prod_ip" ]] || skip "prod pod has no IP"

  # Generate traffic from a client pod inside the cluster (cloud-agnostic:
  # no requirement for pod IPs to be routable from the test host).
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

  # Simulate a crash: force-delete the pod. The Deployment controller
  # immediately schedules a replacement with a new pod IP.
  kubectl delete pod "$old_pod" -n default --grace-period=0 --force 2>&1 | tail -3

  new_ip=$(wait_new_pod_ip "app=kaisel-capture-prod" "default" "$old_ip" 90)
  [[ -n "$new_ip" ]] || fail "no replacement pod IP appeared"
  [[ "$new_ip" != "$old_ip" ]]

  # Monarch's Pod watch must re-reconcile the ShadowTest and rewrite the
  # KaiselRule to the new pod's IP -- no manual intervention, no restart.
  wait_kaiselrule_targetip_is "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS" "$new_ip" 60

  # kaisel's controller-runtime reconciler must diff the updated KaiselRule
  # and push the new IP into the live BPF map -- again, no daemon restart.
  sleep 5

  probe="/post-crash-probe-${RANDOM}"
  kubectl run kaisel-crash-probe --restart=Never --rm -i --image=curlimages/curl:latest \
    -n default -- curl -s --max-time 10 "http://${new_ip}:80${probe}" || true

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success
}
