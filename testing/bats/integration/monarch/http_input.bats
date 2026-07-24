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

@test "ShadowTest CR: status fields reflect a fully reconciled HTTP input stack" {
  local phase shadow_ns siphon_phase igris_ep igris_rmq_phase
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.phase}' 2>/dev/null || true)
  shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
  siphon_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.siphonPhase}' 2>/dev/null || true)
  igris_ep=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.igrisEndpoint}' 2>/dev/null || true)
  igris_rmq_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.status.igrisRabbitMQPhase}' 2>/dev/null || true)

  [[ "$phase" == "Ready" ]] || { echo "phase=${phase}, want Ready" >&2; return 1; }
  [[ "$shadow_ns" == "$SHADOW_NS" ]] || { echo "shadowNamespace=${shadow_ns}, want ${SHADOW_NS}" >&2; return 1; }
  [[ "$siphon_phase" == "Ready" ]] || { echo "siphonPhase=${siphon_phase}, want Ready" >&2; return 1; }
  [[ -n "$igris_ep" ]] || { echo "igrisEndpoint is empty" >&2; return 1; }
  [[ -z "$igris_rmq_phase" ]] || { echo "igrisRabbitMQPhase=${igris_rmq_phase}, want empty for HTTP input" >&2; return 1; }
}

@test "PixieStreamRule: spec content points at the correct shadow stack" {
  local ref active target_ns shadow_ns otel_ep ports
  ref=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.shadowTestRef}' 2>/dev/null || true)
  active=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.active}' 2>/dev/null || true)
  target_ns=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.targetNamespace}' 2>/dev/null || true)
  shadow_ns=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.shadowNamespace}' 2>/dev/null || true)
  otel_ep=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.otelEndpoint}' 2>/dev/null || true)
  ports=$(kubectl get pixiestreamrule "pixie-${SHADOWTEST}" -n "$SHADOWTEST_NS" \
    -o jsonpath='{.spec.targetPorts}' 2>/dev/null || true)

  [[ "$ref" == "${SHADOWTEST_NS}/${SHADOWTEST}" ]] || { echo "shadowTestRef=${ref}, want ${SHADOWTEST_NS}/${SHADOWTEST}" >&2; return 1; }
  [[ "$active" == "true" ]] || { echo "active=${active}, want true" >&2; return 1; }
  [[ "$target_ns" == "$SHADOWTEST_NS" ]] || { echo "targetNamespace=${target_ns}, want ${SHADOWTEST_NS}" >&2; return 1; }
  [[ "$shadow_ns" == "$SHADOW_NS" ]] || { echo "shadowNamespace=${shadow_ns}, want ${SHADOW_NS}" >&2; return 1; }
  [[ -n "$otel_ep" ]] || { echo "otelEndpoint is empty" >&2; return 1; }
  [[ "$ports" == *"80"* ]] || { echo "targetPorts=${ports}, expected to contain 80" >&2; return 1; }
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

@test "recorder: deployment is Available" {
  local avail
  avail=$(kubectl get deployment "${SHADOWTEST}-recorder" -n "$SHADOW_NS" \
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
