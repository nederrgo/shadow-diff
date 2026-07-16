# Monarch integration assertions: pod readiness, CrashLoop detection, diagnostics.
# Part of the integration tier (testing pyramid layer between unit and E2E).
# No Beru, Pixie, or traffic dependencies — only kubectl + k8s API.
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

# Wait for the siphon Deployment (always named 'siphon') to be Available.
monarch_wait_siphon_running() {
  local shadow_ns="$1" timeout="${2:-120}"
  echo "==> [monarch] wait siphon Available in ${shadow_ns}"
  kubectl wait --for=condition=Available deployment/siphon \
    -n "$shadow_ns" --timeout="${timeout}s"
}

# Poll until ShadowTest reaches Failed phase. Returns 0 when Failed is confirmed;
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
