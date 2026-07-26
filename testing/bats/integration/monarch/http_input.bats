#!/usr/bin/env bats
# Monarch integration: HTTP input — verifies that Monarch correctly reconciles a
# ShadowTest with driver: http_request, bringing up the igris-http multicast
# hub and KaiselRule (ingress capture) alongside the three shadow app roles.
#
# Testing pyramid layer: integration (no traffic, no Beru data flow).
# shellcheck shell=bash

load '../../test_helper'

FIXTURE_DIR="${BATS_TEST_DIRNAME}/../../fixtures/integration/monarch-http-input"

setup_file() {
  bats_begin_suite "bats-monarch-http-input" "default"
  ensure_platform_ready

  # Prod target must exist before ShadowTest CR so Monarch can read its container
  # ports during the HTTP ingress capture port-match check.
  echo "==> apply prod target"
  kubectl apply -f "${FIXTURE_DIR}/prod-target.yaml"
  kubectl wait --for=condition=Available deployment/monarch-integ-target \
    -n default --timeout=120s
  bats_suite_mark PROD_DEPLOYED 1

  bats_prepare_shadowtest_slot "$SHADOWTEST" "$SHADOWTEST_NS"
  apply_shadowtest "${FIXTURE_DIR}/shadowtest.yaml"
  bats_suite_mark SHADOWTEST_APPLIED 1

  # --require-kaisel: wait until status.kaiselPhase=Ready (KaiselRule reconciled).
  wait_shadowtest_ready "$SHADOWTEST" "$SHADOWTEST_NS" --require-kaisel

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

@test "kaisel: KaiselRule has targetIPs" {
  monarch_wait_kaisel_rule_ready "kaisel-${SHADOWTEST}" "$SHADOWTEST_NS"
}

@test "ShadowTest CR: status fields reflect a fully reconciled HTTP input stack" {
  local phase shadow_ns kaisel_phase igris_ep igris_rmq_phase
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}' 2>/dev/null || true)
  shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  kaisel_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.kaiselPhase}' 2>/dev/null || true)
  igris_ep=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.igrisEndpoint}' 2>/dev/null || true)
  igris_rmq_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.igrisRabbitMQPhase}' 2>/dev/null || true)

  [[ "$phase" == "Ready" ]] || { echo "phase=${phase}, want Ready" >&2; return 1; }
  [[ "$shadow_ns" == "$SHADOW_NS" ]] || { echo "shadowNamespace=${shadow_ns}, want ${SHADOW_NS}" >&2; return 1; }
  [[ "$kaisel_phase" == "Ready" ]] || { echo "kaiselPhase=${kaisel_phase}, want Ready" >&2; return 1; }
  [[ -n "$igris_ep" ]] || { echo "igrisEndpoint is empty" >&2; return 1; }
  [[ -z "$igris_rmq_phase" ]] || { echo "igrisRabbitMQPhase=${igris_rmq_phase}, want empty for HTTP input" >&2; return 1; }
}

@test "KaiselRule: igris and egress URLs are wired for the shadow namespace" {
  local igris_url egress_url
  igris_url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.igrisBaseURL}' 2>/dev/null || true)
  egress_url=$(kubectl get kaiselrule "kaisel-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.egressBaseURL}' 2>/dev/null || true)

  [[ "$igris_url" == *igris* ]] || { echo "igrisBaseURL=${igris_url}, want an igris URL" >&2; return 1; }
  [[ "$egress_url" == http://shop.* ]] || { echo "egressBaseURL=${egress_url}, want the shadow Shop" >&2; return 1; }
  [[ "$egress_url" == *"${SHADOW_NS}"* ]] || { echo "egressBaseURL not in ${SHADOW_NS}: ${egress_url}" >&2; return 1; }
}

@test "igris-rabbitmq: not deployed for HTTP input" {
  run kubectl get deployment "${SHADOWTEST}-igris-rabbitmq" -n "$SHADOW_NS"
  [ "$status" -ne 0 ]
}

@test "beru-local: deployment is Available" {
  local avail
  avail=$(kubectl get deployment beru-local -n "$SHADOW_NS" \
    -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true)
  [[ "${avail:-0}" -ge 1 ]]
}

@test "shop: deployment is Available" {
  local avail
  avail=$(kubectl get deployment shop -n "$SHADOW_NS" \
    -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true)
  [[ "${avail:-0}" -ge 1 ]]
}

@test "shadow app pods: all roles are Running" {
  local role pod phase
  for role in control-a control-b candidate; do
    pod=$(kubectl get pods -n "$SHADOW_NS" \
      -l "shadow-diff.io/shadowtest-name=${SHADOWTEST},shadow-diff.io/role=${role},shadow-diff.io/resource-kind!=dependency" \
      -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    [[ -n "$pod" ]] || { echo "no pod found for role=${role}" >&2; return 1; }
    phase=$(kubectl get pod "$pod" -n "$SHADOW_NS" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$phase" == "Running" ]] || { echo "pod ${pod} (role=${role}): phase=${phase}" >&2; return 1; }
  done
}

@test "rabbitmq: dependency pods are Available for all roles" {
  local role avail
  for role in control-a control-b candidate; do
    avail=$(kubectl get deployment "rabbitmq-${role}" -n "$SHADOW_NS" \
      -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true)
    [[ "${avail:-0}" -ge 1 ]] || {
      echo "rabbitmq-${role}: no available replicas" >&2
      return 1
    }
  done
}

@test "mongodb: dependency pods are Available for all roles" {
  local role avail
  for role in control-a control-b candidate; do
    avail=$(kubectl get deployment "mongodb-${role}" -n "$SHADOW_NS" \
      -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true)
    [[ "${avail:-0}" -ge 1 ]] || {
      echo "mongodb-${role}: no available replicas" >&2
      return 1
    }
  done
}

teardown_file() {
  bats_teardown_suite "${FIXTURE_DIR}/prod-target.yaml"
}
