#!/usr/bin/env bats
# E2E: Monarch → prod → Kaisel eBPF → igris-http → shadow pods (control-a/b/candidate).
#
# Pipeline under test:
#   1. Monarch lists prod pod IPs into a KaiselRule (+ igrisBaseURL, samplePercentage).
#   2. Kaisel reconciles the CR into live eBPF maps.
#   3. Traced HTTP to those IPs is captured, admitted, and POSTed to igris-http.
#   4. igris multicasts to the three shadow Services; app access logs prove delivery.
#
# Egress (same target, opposite direction):
#   5. The target app calls its dependency, propagating the inbound traceparent.
#   6. Kaisel pairs the outbound request with the inbound response and POSTs the
#      pair to the shadow namespace's Shop as a replayable mock.
#   7. Shadows replay via Envoy shop_ext_proc; Shop async-reports to Beru for
#      HTTP egress diff-of-diffs (protocol=http, direction=egress).
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

  # The dependency the target calls lives in the same fixture.
  kubectl wait --for=condition=Available deployment/kaisel-egress-dep \
    -n default --timeout=120s
  bats_suite_mark EGRESS_DEP_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # Full shadow stack Ready (igris, beru-local, shop, three roles) + KaiselRule.
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
    -n default -- curl -s --max-time 10 "http://${prod_ip}:8080${probe}" || true

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
    "http://${prod_ip}:8080/host-header-test" || true

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
    -n default -- curl -s --max-time 10 "http://${new_ip}:8080${probe}" || true

  sleep 2
  run kaisel_assert_captured "uri=${probe}"
  assert_success
}

# ── Egress capture ─────────────────────────────────────────────────────────

@test "monarch writes egressBaseURL on KaiselRule pointing at the shadow Shop" {
  bats_load_suite_state

  local url
  url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.egressBaseURL}')
  [[ -n "$url" ]] || fail "KaiselRule.spec.egressBaseURL is empty"
  [[ "$url" == http://shop.* ]] || fail "unexpected egressBaseURL: ${url}"
  [[ "$url" == *"${SHADOW_NS}"* ]] || fail "egressBaseURL not in shadow ns ${SHADOW_NS}: ${url}"
}

@test "kaisel records an egress request/response pair as a Shop mock" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset — setup did not reach Ready"

  local trace_id
  run kaisel_prod_egress_call "/dep/echo"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  # The asserted hash is Shop's OWN computed key, so a match proves the record
  # arrived, validated, and was keyed exactly as the ext_proc replay path will
  # look it up — which a 200 from Shop alone would not show.
  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/echo" 90
  assert_success
}

@test "egress mock key retains the query string Envoy looks up" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_call "/dep/echo%3Factive=true"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  # Envoy's :path carries the query, so the seeded key must too — stripping it
  # stores the mock where nothing ever looks.
  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/echo?active=true" 90
  assert_success
}

@test "kaisel records the dependency status code, not a normalized one" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_call "/dep/status/503"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded "status=503" 90
  assert_success
}

@test "scenario large-body: 500KB dependency response is seeded into Shop" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_scenario "large-body"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/size/512000" 90
  assert_success
  # Same Kaisel log line carries body_bytes; 512000 is under the 1 MiB drop cap.
  run kaisel_assert_egress_recorded "body_bytes=512000" 10
  assert_success
}

@test "scenario external: outside-cluster egress is recorded with httpbin.org host" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_scenario "external"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_EXTERNAL_HOST}:/get" 120
  assert_success
}

@test "scenario in-cluster: svc.cluster.local FQDN is the recorded Host" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_scenario "in-cluster"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_FQDN}:/dep/echo" 90
  assert_success
}

@test "scenario parallel: distinct routes under one trace produce distinct mocks" {
  bats_load_suite_state

  local trace_id
  run kaisel_prod_egress_scenario "parallel"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/echo?route=a" 90
  assert_success
  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/echo?route=b" 90
  assert_success
  run kaisel_assert_egress_recorded "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_HOST}:/dep/status/200" 90
  assert_success
}

@test "kaisel seed → Shop → Envoy replay from copied prod traffic" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset — setup did not reach Ready"

  # One prod curl: Kaisel copies ingress (headers + X-Egress-Scenario) to igris,
  # seeds Shop from the prod egress pair, and shadow roles dial the same FQDN
  # path. Shadows retry 599/500 until the mock lands — no separate igris redrive.
  local mark path_enc trace_id
  mark="shopreplay${RANDOM}"
  path_enc="/dep/echo%3Freplay=${mark}"

  run kaisel_prod_egress_scenario "in-cluster" "" "$path_enc"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded \
    "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_FQDN}:/dep/echo?replay=${mark}" 90
  assert_success

  run kaisel_assert_shadow_egress_replay "replay=${mark}"
  assert_success
}

@test "kaisel seed → Shop → Envoy → Beru HTTP egress match" {
  bats_load_suite_state
  [[ -n "${SHADOW_NS:-}" ]] || fail "SHADOW_NS unset — setup did not reach Ready"

  # Full chain: Kaisel seeds Shop from prod egress → shadows replay via Envoy
  # shop_ext_proc → Shop async POSTs /api/v1/egress/diff → Beru diff-of-diffs.
  local mark path_enc trace_id want_sig
  mark="beruegress${RANDOM}"
  path_enc="/dep/echo%3Fberu=${mark}"
  want_sig="http:GET:/dep/echo?beru=${mark}"

  run kaisel_prod_egress_scenario "in-cluster" "" "$path_enc"
  assert_success
  trace_id="$(echo "$output" | tail -1)"

  run kaisel_assert_egress_recorded \
    "trace:${trace_id}:GET:${KAISEL_EGRESS_DEP_FQDN}:/dep/echo?beru=${mark}" 90
  assert_success

  run kaisel_assert_shadow_egress_replay "beru=${mark}"
  assert_success

  # Shop must be wired to beru-local (Monarch injects BERU_HTTP_URL).
  local beru_url
  beru_url=$(kubectl get deploy shop -n "$SHADOW_NS" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="BERU_HTTP_URL")].value}')
  [[ -n "$beru_url" ]] || fail "Shop missing BERU_HTTP_URL"
  [[ "$beru_url" == *beru-local* ]] || fail "Shop BERU_HTTP_URL unexpected: ${beru_url}"

  run beru_wait_http_egress_match "$trace_id" --timeout=120 --signature="$want_sig"
  assert_success
}

@test "kaisel does not record egress for an untraced outbound call" {
  bats_load_suite_state

  local prod_ip marker
  prod_ip=$(kubectl get pod -l "app=${KAISEL_PROD_DEPLOY}" -n default \
    -o jsonpath='{.items[0].status.podIP}')
  [[ -n "$prod_ip" ]] || skip "prod pod has no IP"
  marker="/dep/echo%3Funtraced=${RANDOM}"

  # No traceparent: the mock could not be keyed, and Shop would reject it.
  kubectl run "kaisel-untraced-${RANDOM}" --restart=Never --rm -i \
    --image=curlimages/curl:latest -n default -- \
    curl -sS --max-time 20 "http://${prod_ip}:8080/egress/get?path=${marker}" >/dev/null || true

  sleep 5
  run kaisel_assert_not_captured "egress recorded.*untraced="
  assert_success
}
