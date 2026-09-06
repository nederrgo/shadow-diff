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

# Wait for the ingress hub Deployment to be Available (igris-http or igris-rabbitmq).
monarch_wait_igris_running() {
  local shadow_ns="$1" shadowtest="$2" timeout="${3:-120}"
  local http_name="${shadowtest}-igris" rmq_name="${shadowtest}-igris-rabbitmq"
  echo "==> [monarch] wait igris hub Available: ${http_name} or ${rmq_name} in ${shadow_ns}"
  local elapsed=0
  while true; do
    if kubectl get "deployment/${http_name}" -n "$shadow_ns" >/dev/null 2>&1; then
      kubectl wait --for=condition=Available "deployment/${http_name}" \
        -n "$shadow_ns" --timeout="${timeout}s"
      return $?
    fi
    if kubectl get "deployment/${rmq_name}" -n "$shadow_ns" >/dev/null 2>&1; then
      kubectl wait --for=condition=Available "deployment/${rmq_name}" \
        -n "$shadow_ns" --timeout="${timeout}s"
      return $?
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: neither ${http_name} nor ${rmq_name} found in ${shadow_ns}" >&2
      return 1
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
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
# with a value that includes $want_substr (e.g. 127.0.0.1 for soldier-proxied Mongo).
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

# Assert the shadow role Deployment includes the shadow-soldier sidecar.
monarch_assert_shadow_has_soldier() {
  local shadow_ns="$1" shadowtest="$2" role="$3"
  local deploy names
  deploy="${shadowtest}-${role}"
  names=$(kubectl get deployment "$deploy" -n "$shadow_ns" \
    -o jsonpath='{.spec.template.spec.containers[*].name}' 2>/dev/null || true)
  [[ "$names" == *shadow-soldier* ]] || {
    echo "FAIL: ${deploy} containers=[${names}], want shadow-soldier" >&2
    return 1
  }
  echo "    ${deploy} has shadow-soldier"
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

# Assert no control-a / control-b / candidate Deployments exist (record mode).
# Usage: monarch_assert_no_abc_roles <shadow_ns>
monarch_assert_no_abc_roles() {
  local shadow_ns="$1"
  local abc
  abc=$(kubectl get deploy -n "$shadow_ns" -o name 2>/dev/null \
    | grep -E 'control-a|control-b|candidate' || true)
  if [[ -n "$abc" ]]; then
    echo "FAIL: unexpected ABC Deployments in ${shadow_ns}:" >&2
    echo "$abc" | sed 's/^/  /' >&2
    return 1
  fi
  return 0
}

# Assert KaiselRule is absent (replay mode GC).
# Usage: monarch_assert_no_kaisel_rule <shadowtest_name> [namespace]
monarch_assert_no_kaisel_rule() {
  local name="$1" ns="${2:-default}"
  if kubectl get kaiselrule "kaisel-${name}" -n "$ns" >/dev/null 2>&1; then
    echo "FAIL: KaiselRule kaisel-${name} still present in ${ns} (want absent)" >&2
    return 1
  fi
  return 0
}

# Assert Shop + Igris Deployments have OPERATING_MODE=<mode>.
# Usage: monarch_assert_operating_mode <shadow_ns> <shadowtest_name> <record|replay>
monarch_assert_operating_mode() {
  local shadow_ns="$1" shadowtest="$2" want="$3"
  local op_igris op_shop hub
  if kubectl get deploy "${shadowtest}-igris" -n "$shadow_ns" >/dev/null 2>&1; then
    hub="${shadowtest}-igris"
  elif kubectl get deploy "${shadowtest}-igris-rabbitmq" -n "$shadow_ns" >/dev/null 2>&1; then
    hub="${shadowtest}-igris-rabbitmq"
  else
    echo "FAIL: no igris or igris-rabbitmq Deployment in ${shadow_ns}" >&2
    return 1
  fi
  op_igris=$(kubectl get deploy "$hub" -n "$shadow_ns" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPERATING_MODE")].value}')
  op_shop=$(kubectl get deploy shop -n "$shadow_ns" \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPERATING_MODE")].value}')
  [[ "$op_igris" == "$want" ]] || {
    echo "FAIL: ${hub} OPERATING_MODE=${op_igris}, want ${want}" >&2
    return 1
  }
  [[ "$op_shop" == "$want" ]] || {
    echo "FAIL: shop OPERATING_MODE=${op_shop}, want ${want}" >&2
    return 1
  }
  return 0
}

# Patch ShadowTest.spec.mode (and optional sessionID for replay).
# Usage: monarch_patch_mode <name> <ns> <record|replay> [session_id]
monarch_patch_mode() {
  local name="$1" ns="$2" mode="$3" session="${4:-}"
  local patch
  if [[ "$mode" == "replay" ]]; then
    [[ -n "$session" ]] || {
      echo "monarch_patch_mode: session_id required for replay" >&2
      return 1
    }
    patch=$(printf '{"spec":{"mode":"replay","sessionID":"%s"}}' "$session")
  else
    patch='{"spec":{"mode":"record"}}'
  fi
  echo "==> [monarch] patch ${ns}/${name} mode=${mode}${session:+ session=${session}}"
  kubectl patch shadowtest "$name" -n "$ns" --type=merge -p "$patch"
}

# Wait until KaiselRule is gone (replay GC).
# Usage: monarch_wait_no_kaisel_rule <shadowtest_name> [namespace] [timeout_seconds]
monarch_wait_no_kaisel_rule() {
  local name="$1" ns="${2:-default}" timeout="${3:-120}"
  local elapsed=0
  echo "==> [monarch] wait KaiselRule kaisel-${name} absent"
  while true; do
    if ! kubectl get kaiselrule "kaisel-${name}" -n "$ns" >/dev/null 2>&1; then
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: KaiselRule kaisel-${name} still present after ${timeout}s" >&2
      return 1
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
}

# Wait until no ABC Deployments remain (record GC).
# Usage: monarch_wait_no_abc_roles <shadow_ns> [timeout_seconds]
monarch_wait_no_abc_roles() {
  local shadow_ns="$1" timeout="${2:-120}"
  local elapsed=0 abc
  echo "==> [monarch] wait ABC roles gone in ${shadow_ns}"
  while true; do
    abc=$(kubectl get deploy -n "$shadow_ns" -o name 2>/dev/null \
      | grep -E 'control-a|control-b|candidate' || true)
    if [[ -z "$abc" ]]; then
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: ABC Deployments still present after ${timeout}s:" >&2
      echo "$abc" | sed 's/^/  /' >&2
      return 1
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
}

# Wait until Shop + Igris Deployments show OPERATING_MODE=<mode>.
# Usage: monarch_wait_operating_mode <shadow_ns> <shadowtest> <record|replay> [timeout]
monarch_wait_operating_mode() {
  local shadow_ns="$1" shadowtest="$2" want="$3" timeout="${4:-180}"
  local elapsed=0
  echo "==> [monarch] wait OPERATING_MODE=${want} on igris+shop"
  while true; do
    if monarch_assert_operating_mode "$shadow_ns" "$shadowtest" "$want" 2>/dev/null; then
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      monarch_assert_operating_mode "$shadow_ns" "$shadowtest" "$want"
      return 1
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
}

# Wait until Monarch has triggered Igris replay (status.replayState=started).
# OPERATING_MODE=replay on the Deployment spec is not enough — maybeTriggerReplay
# runs on a later reconcile after roll-ready.
# Usage: monarch_wait_replay_started <name> [namespace] [timeout_seconds]
monarch_wait_replay_started() {
  local name="$1" ns="${2:-default}" timeout="${3:-180}"
  local elapsed=0 replay
  echo "==> [monarch] wait replayState=started on ${ns}/${name}"
  while true; do
    replay=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.replayState}' 2>/dev/null || true)
    if [[ "$replay" == "started" ]]; then
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      echo "FAIL: replayState=${replay:-<empty>}, want started after ${timeout}s" >&2
      kubectl get shadowtest "$name" -n "$ns" \
        -o jsonpath='phase={.status.phase} kaisel={.status.kaiselPhase} replay={.status.replayState} msg={.status.message}{"\n"}' >&2 || true
      return 1
    fi
    echo "    waiting replayState (got=${replay:-<empty>}) (${elapsed}s/${timeout}s)..."
    sleep 2
    elapsed=$((elapsed + 2))
  done
}


# Poll until ShadowTest status.amqpQueueName is set (prod shadow queue declared).
# On success exports BOOT_FAIL_AMQP_QUEUE. Falls back to shadow-diff-<uid> if status
# is empty but the CR has a UID (Monarch naming convention).
monarch_wait_amqp_queue_name() {
  local name="$1" ns="${2:-default}" timeout="${3:-60}"
  local elapsed=0 queue uid

  echo "==> [monarch] wait amqpQueueName: ${ns}/${name} (timeout=${timeout}s)"
  while true; do
    queue=$(kubectl get shadowtest "$name" -n "$ns" \
      -o jsonpath='{.status.amqpQueueName}' 2>/dev/null || true)
    if [[ -n "$queue" ]]; then
      echo "    amqpQueueName=${queue}"
      export BOOT_FAIL_AMQP_QUEUE="$queue"
      return 0
    fi
    if [[ "$elapsed" -ge "$timeout" ]]; then
      uid=$(kubectl get shadowtest "$name" -n "$ns" \
        -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
      if [[ -n "$uid" ]]; then
        queue="shadow-diff-$(echo "$uid" | tr '[:upper:]' '[:lower:]')"
        echo "    amqpQueueName unset; using convention ${queue}"
        export BOOT_FAIL_AMQP_QUEUE="$queue"
        return 0
      fi
      echo "FAIL: timed out waiting for amqpQueueName on ${ns}/${name}" >&2
      return 1
    fi
    echo "    wait amqpQueueName (${elapsed}/${timeout}s)"
    sleep 5
    elapsed=$((elapsed + 5))
  done
}

# Assert the named queue is absent on rmq-prod-broker (rabbitmqadmin).
monarch_assert_prod_queue_absent() {
  local queue="$1"
  local broker_pod listing
  [[ -n "$queue" ]] || {
    echo "monarch_assert_prod_queue_absent: empty queue name" >&2
    return 1
  }
  broker_pod=$(kubectl get pods -n default -l app=rmq-prod-broker \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -z "$broker_pod" ]]; then
    echo "monarch_assert_prod_queue_absent: rmq-prod-broker pod not found" >&2
    return 1
  fi
  listing=$(kubectl exec -n default "$broker_pod" -- \
    rabbitmqadmin list queues name 2>/dev/null || true)
  if echo "$listing" | grep -F "$queue" >/dev/null 2>&1; then
    echo "FAIL: prod queue ${queue} still present on rmq-prod-broker" >&2
    echo "$listing" >&2
    return 1
  fi
  echo "    prod queue ${queue} absent"
  return 0
}

# Return the rmq-prod-broker pod name (default ns).
monarch_prod_broker_pod() {
  local broker_pod
  broker_pod=$(kubectl get pods -n default -l app=rmq-prod-broker \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -z "$broker_pod" ]]; then
    echo "monarch_prod_broker_pod: rmq-prod-broker pod not found" >&2
    return 1
  fi
  echo "$broker_pod"
}

# Declare a durable queue with args that conflict with Monarch's prodShadowQueueArgs
# (x-max-length: 500) so the controller's QueueDeclare returns PRECONDITION_FAILED.
monarch_declare_conflicting_prod_queue() {
  local queue="$1"
  local broker_pod
  [[ -n "$queue" ]] || {
    echo "monarch_declare_conflicting_prod_queue: empty queue name" >&2
    return 1
  }
  broker_pod=$(monarch_prod_broker_pod) || return 1
  echo "==> [monarch] declare conflicting prod queue ${queue}"
  kubectl exec -n default "$broker_pod" -- \
    rabbitmqadmin declare queue name="$queue" durable=true \
    arguments='{"x-max-length":1,"x-overflow":"drop-head"}'
}

# Scale monarch-controller-manager and wait for the desired replica state.
monarch_scale_controller() {
  local replicas="$1"
  echo "==> [monarch] scale controller-manager to ${replicas}"
  kubectl scale deployment/monarch-controller-manager -n monarch-system --replicas="$replicas"
  if [[ "$replicas" -eq 0 ]]; then
    local elapsed=0
    while kubectl get pods -n monarch-system -l control-plane=controller-manager \
      --no-headers 2>/dev/null | grep -q .; do
      if [[ "$elapsed" -ge 120 ]]; then
        echo "FAIL: controller pods still present after scale to 0" >&2
        return 1
      fi
      sleep 2
      elapsed=$((elapsed + 2))
    done
    echo "    controller scaled to 0"
    return 0
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s
}
