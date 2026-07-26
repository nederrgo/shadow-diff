# Monarch integration assertions: pod readiness, CrashLoop detection, diagnostics.
# Part of the integration tier (testing pyramid layer between unit and E2E).
# No Beru or traffic dependencies — only kubectl + k8s API.
# shellcheck shell=bash

# Bad container waiting reasons that indicate a failed pod (not just slow start).
_MONARCH_BAD_REASONS="CrashLoopBackOff|OOMKilled|Error|ImagePullBackOff|ErrImagePull|InvalidImageName"

# Wait for all Deployments in shadow_ns to reach Available condition.
monarch_wait_shadow_ready() {
  local shadow_ns="$1" timeout="${2:-180}"
  echo "==> [monarch] wait all deployments Available in ${shadow_ns} (timeout=${timeout}s)"
  kubectl wait --for=condition=Available deployment --all \
    -n "$shadow_ns" --timeout="${timeout}s"
}

# Fail immediately if any pod in shadow_ns has a bad container waiting reason.
# Checks phase=Failed and container waiting reasons (CrashLoopBackOff, ImagePullBackOff, …).
monarch_assert_no_crashloop() {
  local shadow_ns="$1"
  local pods bad_pods pod reason
  pods=$(kubectl get pods -n "$shadow_ns" \
    -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase} {range .status.containerStatuses[*]}{.state.waiting.reason}{end}{"\n"}{end}' \
    2>/dev/null || true)

  bad_pods=$(echo "$pods" | grep -E "$_MONARCH_BAD_REASONS" || true)
  if [[ -z "$bad_pods" ]]; then
    return 0
  fi

  echo "==> [monarch] BAD PODS in ${shadow_ns}:" >&2
  echo "$bad_pods" >&2

  # Print last log lines for each bad pod to aid diagnosis.
  while IFS= read -r line; do
    pod=$(echo "$line" | awk '{print $1}')
    [[ -z "$pod" ]] && continue
    echo "--- logs: ${pod} ---" >&2
    kubectl logs -n "$shadow_ns" "$pod" --tail=20 2>/dev/null >&2 || true
  done <<< "$bad_pods"

  echo "FAIL: pods in CrashLoop/Error state in ${shadow_ns}" >&2
  return 1
}

# Poll until the app pod for a given role (control-a|control-b|candidate) is Running.
# Fails fast on bad waiting reasons; times out after $timeout seconds.
monarch_wait_app_pod_running() {
  local shadow_ns="$1" shadowtest="$2" role="$3" timeout="${4:-120}"
  bats_source_e2e_helpers

  local pod phase reason deadline elapsed=0
  deadline=$timeout

  echo "==> [monarch] wait pod Running: role=${role} ns=${shadow_ns}"
  while true; do
    pod=$(shadow_app_pod_for_role "$shadow_ns" "$shadowtest" "$role" 2>/dev/null || true)

    if [[ -n "$pod" ]]; then
      phase=$(kubectl get pod "$pod" -n "$shadow_ns" \
        -o jsonpath='{.status.phase}' 2>/dev/null || true)
      reason=$(kubectl get pod "$pod" -n "$shadow_ns" \
        -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null || true)

      if [[ "$phase" == "Running" ]]; then
        echo "    pod ${pod} Running"
        return 0
      fi

      if echo "$reason" | grep -qE "$_MONARCH_BAD_REASONS"; then
        echo "FAIL: pod ${pod} (role=${role}) stuck in ${reason}" >&2
        kubectl logs -n "$shadow_ns" "$pod" --tail=30 2>/dev/null >&2 || true
        return 1
      fi

      echo "    waiting pod (role=${role}) phase=${phase:-<none>} reason=${reason:-<none>} (${elapsed}s/${deadline}s)"
    else
      echo "    waiting pod (role=${role}) not found yet (${elapsed}s/${deadline}s)"
    fi

    if [[ "$elapsed" -ge "$deadline" ]]; then
      echo "FAIL: timed out waiting for pod role=${role} in ${shadow_ns}" >&2
      monarch_dump_diagnostics "$shadow_ns"
      return 1
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done
}

# Wait for all three shadow app roles to be Running.
monarch_wait_all_roles_running() {
  local shadow_ns="$1" shadowtest="$2" timeout="${3:-180}"
  local role
  for role in control-a control-b candidate; do
    monarch_wait_app_pod_running "$shadow_ns" "$shadowtest" "$role" "$timeout" || return 1
  done
}

# Wait for the igris-http Deployment (${shadowtest}-igris) to be Available.
monarch_wait_igris_running() {
  local shadow_ns="$1" shadowtest="$2" timeout="${3:-120}"
  echo "==> [monarch] wait igris Available: ${shadowtest}-igris in ${shadow_ns}"
  kubectl wait --for=condition=Available "deployment/${shadowtest}-igris" \
    -n "$shadow_ns" --timeout="${timeout}s"
}

# Wait for KaiselRule to have at least one target IP (ingress capture wired).
monarch_wait_kaisel_rule_ready() {
  local name="$1" ns="${2:-default}" timeout="${3:-120}"
  local elapsed=0 ip
  echo "==> [monarch] wait KaiselRule ${ns}/${name} has targetIPs"
  while true; do
    ip=$(kubectl get kaiselrule "$name" -n "$ns" \
      -o jsonpath='{.spec.targetIPs[0]}' 2>/dev/null || true)
    if [[ -n "$ip" ]]; then
      echo "    KaiselRule ${name} targetIPs[0]=${ip}"
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for KaiselRule ${ns}/${name}" >&2
      return 1
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

# Poll until ShadowTest Failed phase. Returns 0 when Failed is confirmed;
# returns 1 on timeout or if phase unexpectedly becomes Ready.
# Exports SHADOWTEST_FAIL_MESSAGE with the status message on success.
monarch_wait_shadowtest_failed() {
  local name="$1" ns="${2:-default}" timeout="${3:-60}"
  local elapsed=0 phase message

  echo "==> [monarch] wait ShadowTest Failed: ${ns}/${name} (timeout=${timeout}s)"
  while true; do
    phase=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    message=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.message}' 2>/dev/null || true)

    if [[ "$phase" == "Failed" ]]; then
      echo "    ShadowTest phase=Failed message=${message}"
      export SHADOWTEST_FAIL_MESSAGE="$message"
      return 0
    fi

    if [[ "$phase" == "Ready" ]]; then
      echo "FAIL: ShadowTest unexpectedly reached Ready (expected Failed)" >&2
      kubectl get shadowtest "$name" -n "$ns" -o yaml 2>/dev/null >&2 || true
      return 1
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for ShadowTest ${ns}/${name} to fail (phase=${phase:-<none>})" >&2
      return 1
    fi

    echo "    wait Failed (${elapsed}/${timeout}s) phase=${phase:-<none>}"
    sleep 5
    elapsed=$((elapsed + 5))
  done
}

# Wait until bring-up has started far enough that delete exercises real cleanup:
# status.shadowNamespace is set and the namespace object exists.
# Phase may be Progressing or already Ready (race); callers assert cleanup, not stage freeze.
monarch_wait_shadowtest_bringup_started() {
  local name="$1" ns="${2:-default}" timeout="${3:-120}"
  local elapsed=0 phase shadow_ns

  echo "==> [monarch] wait ShadowTest bring-up started: ${ns}/${name} (timeout=${timeout}s)"
  while true; do
    phase=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)

    if [[ -n "$shadow_ns" ]] && kubectl get namespace "$shadow_ns" >/dev/null 2>&1; then
      echo "    bring-up started phase=${phase:-<none>} shadowNamespace=${shadow_ns}"
      export SHADOW_NS="$shadow_ns"
      return 0
    fi

    if [[ "$phase" == "Failed" ]]; then
      local message
      message=$(kubectl get shadowtest "$name" -n "$ns" \
        -o jsonpath='{.status.message}' 2>/dev/null || true)
      echo "FAIL: ShadowTest Failed before bring-up started: ${message}" >&2
      return 1
    fi

    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: timed out waiting for bring-up start ${ns}/${name} (phase=${phase:-<none>} ns=${shadow_ns:-<none>})" >&2
      return 1
    fi

    echo "    wait bring-up (${elapsed}/${timeout}s) phase=${phase:-<none>} ns=${shadow_ns:-<none>}"
    sleep 2
    elapsed=$((elapsed + 2))
  done
}

# After delete: ShadowTest CR, shadow namespace, and KaiselRule are all gone.
monarch_wait_shadowtest_cleaned() {
  local name="$1" ns="${2:-default}" timeout="${3:-180}"
  local shadow_ns="shadow-${ns}-${name}"
  local rule="kaisel-${name}"

  bats_source_e2e_helpers
  echo "==> [monarch] wait ShadowTest cleaned: ${ns}/${name} (timeout=${timeout}s)"

  wait_shadowtest_gone "$name" "$ns" "$timeout" || return 1
  assert_kubectl_not_found shadowtest "$name" -n "$ns" || return 1
  wait_shadow_namespace_gone "$shadow_ns" "$timeout" || return 1
  assert_kubectl_not_found namespace "$shadow_ns" || return 1
  assert_kubectl_not_found kaiselrule "$rule" -n "$ns" || return 1
  echo "    cleaned: CR + ${shadow_ns} + ${rule}"
}

# Wait until dependency Deployments are Available for all three roles.
monarch_wait_dependency_available() {
  local shadow_ns="$1" dep_name="$2" timeout="${3:-180}"
  local role
  echo "==> [monarch] wait dependency Available: ${dep_name}-{role} in ${shadow_ns}"
  for role in control-a control-b candidate; do
    kubectl wait --for=condition=Available "deployment/${dep_name}-${role}" \
      -n "$shadow_ns" --timeout="${timeout}s" || return 1
  done
}

# Print space-separated UIDs of current shadow app pods (control-a control-b candidate).
monarch_shadow_app_pod_uids() {
  local shadow_ns="$1" shadowtest="$2"
  local role pod uid
  bats_source_e2e_helpers
  for role in control-a control-b candidate; do
    pod=$(shadow_app_pod_for_role "$shadow_ns" "$shadowtest" "$role" 2>/dev/null || true)
    uid=""
    if [[ -n "$pod" ]]; then
      uid=$(kubectl get pod "$pod" -n "$shadow_ns" \
        -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
    fi
    printf '%s ' "${uid:-missing}"
  done
  printf '\n'
}

# Assert the app container env on the shadow Deployment for $role contains $env_name
# with a value that includes $want_substr (e.g. mongodb-control-a).
monarch_assert_shadow_app_env() {
  local shadow_ns="$1" shadowtest="$2" role="$3" env_name="$4" want_substr="$5"
  local deploy env_val
  deploy="${shadowtest}-${role}"
  env_val=$(kubectl get deployment "$deploy" -n "$shadow_ns" -o json 2>/dev/null \
    | jq -r --arg n "$env_name" '
        .spec.template.spec.containers[]
        | select(.name=="app")
        | (.env // [])[]
        | select(.name==$n)
        | .value // empty
      ' 2>/dev/null || true)
  [[ -n "$env_val" ]] || {
    echo "FAIL: ${deploy} app env missing ${env_name}" >&2
    kubectl get deployment "$deploy" -n "$shadow_ns" -o json 2>/dev/null \
      | jq -c '.spec.template.spec.containers[] | select(.name=="app") | .env' >&2 || true
    return 1
  }
  [[ "$env_val" == *"$want_substr"* ]] || {
    echo "FAIL: ${deploy} ${env_name}=${env_val}, want substring ${want_substr}" >&2
    return 1
  }
  echo "    ${deploy} ${env_name}=${env_val}"
}

# Print pod table and describe any non-Running pod. Called on failure for context.
monarch_dump_diagnostics() {
  local shadow_ns="$1"
  echo "==> [monarch] pod diagnostics: ${shadow_ns}" >&2
  kubectl get pods -n "$shadow_ns" -o wide 2>/dev/null >&2 || true

  local pod phase
  while IFS= read -r pod; do
    [[ -z "$pod" ]] && continue
    phase=$(kubectl get pod "$pod" -n "$shadow_ns" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ "$phase" != "Running" ]]; then
      echo "--- describe: ${pod} ---" >&2
      kubectl describe pod "$pod" -n "$shadow_ns" 2>/dev/null >&2 || true
    fi
  done < <(kubectl get pods -n "$shadow_ns" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
}
