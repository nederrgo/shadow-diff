# OTLP ingress path: prod echo-http (tap target) -> Pixie eBPF -> Siphon -> igris-http -> 3 shadows.
# Source from E2E scripts; do not execute directly.

shadow_igris_cluster_url() {
  local shadow_ns="$1" shadowtest="$2" port="${3:-80}"
  echo "http://${shadowtest}-igris.${shadow_ns}.svc.cluster.local:${port}"
}

prod_service_host() {
  local prod_ns="${1:-default}" svc="${2:-my-prod-app}"
  echo "${svc}.${prod_ns}.svc.cluster.local"
}

wait_prod_echo_ready() {
  local prod_ns="${1:-default}" label="${2:-app=my-prod-app}" max_wait="${3:-120}"
  kubectl wait -n "$prod_ns" --for=condition=Ready pod -l "$label" --timeout="${max_wait}s" >/dev/null
  kubectl get svc -n "$prod_ns" my-prod-app >/dev/null 2>&1 || {
    echo "ERROR: prod Service my-prod-app missing in ${prod_ns}" >&2
    return 1
  }
  prod_service_host "$prod_ns" my-prod-app
}

wait_pixie_stream_rule() {
  local shadowtest="$1" shadowtest_ns="$2" max_wait="${3:-120}"
  local name="pixie-${shadowtest}" i=0 phase="" active=""
  while [[ "$i" -lt "$max_wait" ]]; do
    if kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" >/dev/null 2>&1; then
      phase=$(kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
      active=$(kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" -o jsonpath='{.spec.active}' 2>/dev/null || true)
      labels=$(kubectl get pixiestreamrule "$name" -n "$shadowtest_ns" -o jsonpath='{.spec.targetLabels}' 2>/dev/null || true)
      if [[ "$active" == "true" && ( "$phase" == "Active" || -z "$phase" ) ]]; then
        echo "    PixieStreamRule ${shadowtest_ns}/${name} active targetLabels=${labels}"
        return 0
      fi
    fi
    echo "    waiting for PixieStreamRule ${name} (${i}s/${max_wait}s)"
    sleep 2
    i=$((i + 2))
  done
  echo "ERROR: PixieStreamRule pixie-${shadowtest} not active" >&2
  return 1
}

wait_pixie_vizier_pem() {
  local max_wait="${1:-120}" i=0
  while [[ "$i" -lt "$max_wait" ]]; do
    if kubectl get pods -n pl -l name=vizier-pem --no-headers 2>/dev/null | awk '$3=="Running"{ok=1} END{exit !ok}'; then
      echo "    Pixie PEM Running in pl"
      return 0
    fi
    echo "    waiting for Pixie PEM (${i}s/${max_wait}s)"
    sleep 3
    i=$((i + 3))
  done
  echo "ERROR: Pixie PEM not Running — run ./testing/scripts/setup-local-pixie.sh" >&2
  return 1
}

ensure_shadow_siphon_deployment() {
  local shadow_ns="$1" shadowtest="$2" igris_port="${3:-80}"
  local igris_url manifest tmp
  igris_url=$(shadow_igris_cluster_url "$shadow_ns" "$shadowtest" "$igris_port")
  manifest="$(dirname "${BASH_SOURCE[0]}")/../manifests/siphon-otlp-e2e/siphon-deployment.yaml"
  tmp=$(mktemp)
  sed -e "s|__IGRIS_BASE_URL__|${igris_url}|g" "$manifest" >"$tmp"
  kubectl apply -n "$shadow_ns" -f "$tmp"
  rm -f "$tmp"
  kubectl rollout status deployment/siphon -n "$shadow_ns" --timeout=120s
  kubectl patch service/siphon -n "$shadow_ns" --type=merge \
    -p '{"spec":{"selector":{"app.kubernetes.io/name":"siphon"}}}' >/dev/null
  echo "    siphon OTLP receiver ready in ${shadow_ns} -> igris ${igris_url}"
}

siphon_otlp_cluster_addr() {
  local shadow_ns="$1"
  echo "siphon.${shadow_ns}.svc.cluster.local:4317"
}

curl_prod_service() {
  local prod_ns="$1" trace="$2" path="${3:-/}" svc="${4:-my-prod-app}"
  local host name phase logs exit_code i=0
  host=$(prod_service_host "$prod_ns" "$svc")
  name="prod-hit-${trace//[^a-zA-Z0-9]/-}"
  name="${name:0:50}"

  kubectl delete pod "$name" -n "$prod_ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true

  # ponytail: kubectl run --rm -i races image pull / TTY on WSL; create pod, wait, read logs
  kubectl run "$name" --restart=Never -n "$prod_ns" \
    --image=curlimages/curl:8.5.0 \
    --image-pull-policy=IfNotPresent \
    --command -- curl -sf -o /dev/null -w "prod_http=%{http_code}\n" \
    -H "x-shadow-trace-id: ${trace}" \
    "http://${host}:80${path}"

  # ponytail: one-shot curl pods go Succeeded, never stay Ready — poll phase, not Ready
  local phase=""
  while [[ "$i" -lt 180 ]]; do
    phase=$(kubectl get pod "$name" -n "$prod_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in
      Succeeded|Failed) break ;;
    esac
    sleep 1
    i=$((i + 1))
  done

  if [[ "$phase" != "Succeeded" && "$phase" != "Failed" ]]; then
    kubectl describe pod "$name" -n "$prod_ns" 2>&1 | tail -15 >&2 || true
    kubectl delete pod "$name" -n "$prod_ns" --ignore-not-found >/dev/null 2>&1 || true
    echo "prod_http=000" >&2
    return 1
  fi

  logs=$(kubectl logs -n "$prod_ns" "$name" 2>/dev/null || true)
  exit_code=$(kubectl get pod "$name" -n "$prod_ns" \
    -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null || echo 1)
  kubectl delete pod "$name" -n "$prod_ns" --ignore-not-found >/dev/null 2>&1 || true

  if [[ -n "$logs" ]]; then
    echo "$logs"
  fi
  if [[ "$exit_code" != "0" ]] || ! echo "$logs" | grep -qE 'prod_http=2[0-9]{2}'; then
    echo "ERROR: prod curl failed (exit=${exit_code}, host=${host}, path=${path})" >&2
    return 1
  fi
}

send_otlp_http_log() {
  local addr="$1" trace="$2" method="${3:-GET}" path="${4:-/}" body="${5:-}"
  local repo lib
  repo="${REPO:-$(cd "$(dirname "${BASH_SOURCE[1]}")/../.." && pwd)}"
  lib="${repo}/testing/scripts/lib/otlp_log_send.go"
  (cd "${repo}/pipeline/siphon" && go run "$lib" -addr "$addr" -trace "$trace" -method "$method" -path "$path" -body "$body")
}

wait_igris_multicast_trace() {
  local shadow_ns="$1" shadowtest="$2" trace="$3" max_wait="${4:-60}"
  local deploy="${shadowtest}-igris" i=0 logs
  while [[ "$i" -lt "$max_wait" ]]; do
    logs=$(kubectl logs -n "$shadow_ns" "deployment/${deploy}" --tail=400 2>/dev/null || true)
    if echo "$logs" | grep -Fq "multicast complete" \
      && { echo "$logs" | grep -Fq "trace_id=${trace}" || echo "$logs" | grep -Fq "$trace"; }; then
      return 0
    fi
    sleep 2
    i=$((i + 2))
  done
  echo "ERROR: Igris did not log multicast complete for trace ${trace}" >&2
  kubectl logs -n "$shadow_ns" "deployment/${deploy}" --tail=40 2>&1 | sed 's/^/       /' >&2 || true
  return 1
}

assert_shadow_roles_saw_trace() {
  local shadow_ns="$1" shadowtest="$2" trace="$3"
  local role pod logs ok=0
  for role in control-a control-b candidate; do
    pod=$(shadow_app_pod_for_role "$shadow_ns" "$shadowtest" "$role" || true)
    if [[ -z "$pod" ]]; then
      echo "ERROR: no shadow app pod for role ${role}" >&2
      return 1
    fi
    logs=$(kubectl logs -n "$shadow_ns" "$pod" -c app --tail=120 2>/dev/null || true)
    if echo "$logs" | grep -Fq "$trace"; then
      echo "    ${role}: saw trace ${trace}"
      ok=$((ok + 1))
    else
      echo "    ${role}: trace ${trace} not found in app logs" >&2
    fi
  done
  [[ "$ok" -eq 3 ]]
}
