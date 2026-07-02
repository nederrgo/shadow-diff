# Pixie Vizier install + PixieStreamRule PxL bridge helpers.
# Source from setup-local-pixie.sh and pixie-stream-bridge.sh; do not execute directly.

PIXIE_NAMESPACE="${PIXIE_NAMESPACE:-pl}"
PIXIE_CLUSTER_NAME="${PIXIE_CLUSTER_NAME:-monarch-local}"
PIXIE_EXPORT_INTERVAL_SEC="${PIXIE_EXPORT_INTERVAL_SEC:-3}"
PIXIE_BRIDGE_STATE_DIR="${PIXIE_BRIDGE_STATE_DIR:-${REPO:-.}/.cache/pixie-bridge}"

pixie_supported_minikube_driver() {
  case "${1:-}" in
    kvm2|virtualbox|hyperkit) return 0 ;;
    *) return 1 ;;
  esac
}

require_pixie_minikube_driver() {
  local driver="${MINIKUBE_DRIVER:-}"
  if [[ -z "$driver" ]] && command -v minikube >/dev/null 2>&1; then
    driver=$(minikube_profile_driver)
  fi
  if [[ "$driver" == kind ]] || kubectl config current-context 2>/dev/null | grep -qi kind; then
    echo "ERROR: Pixie requires Minikube with a VM driver (kvm2/virtualbox), not Kind" >&2
    exit 1
  fi
  if ! pixie_supported_minikube_driver "$driver"; then
    echo "ERROR: Pixie requires MINIKUBE_DRIVER=kvm2 or virtualbox (current: ${driver:-unknown})" >&2
    echo "       Pixie does not support minikube driver=docker, driver=none, or Kind." >&2
    exit 1
  fi
  MINIKUBE_DRIVER="$driver"
  export MINIKUBE_DRIVER
}

minikube_profile_driver() {
  minikube -p "${MINIKUBE_PROFILE:-minikube}" config get driver 2>/dev/null || true
}

minikube_profile_exists() {
  minikube profile list -o json 2>/dev/null | jq -e --arg p "${MINIKUBE_PROFILE:-minikube}" \
    '.valid[] | select(.Name == $p)' >/dev/null 2>&1
}

# Pick kvm2/virtualbox for Pixie when caller did not set a VM driver.
resolve_pixie_minikube_driver() {
  local driver="${MINIKUBE_DRIVER:-}"
  if pixie_supported_minikube_driver "$driver"; then
    echo "$driver"
    return 0
  fi
  # shellcheck source=testing/scripts/lib/cluster-minikube.sh
  if _kvm2_available 2>/dev/null; then
    echo kvm2
    return 0
  fi
  if _vbox_available 2>/dev/null; then
    echo virtualbox
    return 0
  fi
  return 1
}

# Fail before minikube start when profile was created with a different driver (e.g. none → kvm2).
assert_minikube_driver_compatible() {
  local want="${1:-${MINIKUBE_DRIVER:-}}"
  local profile="${MINIKUBE_PROFILE:-minikube}"
  local have=""
  [[ -n "$want" ]] || { echo "ERROR: assert_minikube_driver_compatible: no target driver" >&2; return 1; }
  if ! minikube_profile_exists; then
    return 0
  fi
  have=$(minikube_profile_driver)
  if [[ -z "$have" ]]; then
    return 0
  fi
  if [[ "$have" == "$want" ]]; then
    return 0
  fi
  echo "ERROR: minikube profile '${profile}' was created with driver=${have}, but Pixie needs driver=${want}" >&2
  echo "       Delete and recreate the cluster (one-time):" >&2
  echo "         minikube delete -p ${profile}" >&2
  echo "         MINIKUBE_DRIVER=${want} ./testing/scripts/setup-local-pixie.sh" >&2
  echo "       Or for full E2E:" >&2
  echo "         minikube delete -p ${profile}" >&2
  echo "         MINIKUBE_DRIVER=${want} ./testing/scripts/e2e-reset-minikube.sh --setup-pixie --run-otlp-ingress-test" >&2
  return 1
}

# Fail before minikube start when profile CNI differs (e.g. calico cluster + flannel Pixie path).
assert_minikube_cni_compatible() {
  local want="${1:-${MINIKUBE_CNI:-flannel}}"
  local profile="${MINIKUBE_PROFILE:-minikube}"
  local have=""
  if ! minikube_profile_exists; then
    return 0
  fi
  have=$(minikube -p "$profile" config get cni 2>/dev/null || true)
  [[ -z "$have" || "$have" == "$want" ]] && return 0
  echo "ERROR: minikube profile '${profile}' has cni=${have}, but Pixie E2E needs cni=${want}" >&2
  echo "       Delete and recreate (one-time):" >&2
  echo "         minikube delete -p ${profile}" >&2
  echo "         ./testing/scripts/e2e-reset-minikube.sh" >&2
  return 1
}

verify_pixie_ebpf_ready() {
  local profile="${MINIKUBE_PROFILE:-minikube}"
  echo "==> Verify eBPF prerequisites (minikube profile=${profile})"
  if ! minikube -p "$profile" status --format='{{.Host}}' 2>/dev/null | grep -qi running; then
    echo "ERROR: Minikube profile '${profile}' is not running" >&2
    exit 1
  fi
  if [[ "${MINIKUBE_DRIVER:-}" == kvm2 ]] && [[ ! -r /dev/kvm ]]; then
    echo "ERROR: /dev/kvm not readable — enable nested virt for kvm2" >&2
    exit 1
  fi
  if ! minikube -p "$profile" ssh -- sudo test -d /sys/kernel/debug/tracing 2>/dev/null; then
    echo "ERROR: /sys/kernel/debug/tracing missing inside minikube node" >&2
    echo "       Try: minikube -p ${profile} ssh -- sudo mount -t debugfs debugfs /sys/kernel/debug" >&2
    exit 1
  fi
  if ! minikube -p "$profile" ssh -- 'mount | grep -q debugfs' 2>/dev/null; then
    echo "WARN: debugfs not mounted in minikube node — Pixie PEM may fail to attach BPF" >&2
  else
    echo "    debugfs mounted; /sys/kernel/debug/tracing present"
  fi
}

ensure_px_cli() {
  if command -v px >/dev/null 2>&1; then
    return 0
  fi
  echo "==> Install Pixie CLI (px)"
  bash -c "$(curl -fsSL https://withpixie.ai/install.sh)"
  command -v px >/dev/null 2>&1 || {
    echo "ERROR: px CLI not found after install" >&2
    exit 1
  }
}

ensure_pixie_auth() {
  if [[ -n "${PIXIE_API_KEY:-}" ]]; then
    return 0
  fi
  if px get viziers >/dev/null 2>&1; then
    return 0
  fi
  echo "ERROR: Pixie auth required (free Pixie Cloud account)" >&2
  echo "       Interactive: px auth login --manual" >&2
  echo "       Background:  export PIXIE_API_KEY=... before nohup" >&2
  exit 1
}

ensure_pixie_deploy_key() {
  if [[ -n "${PIXIE_DEPLOY_KEY:-}" ]]; then
    return 0
  fi
  PIXIE_DEPLOY_KEY=$(px deploy-key list 2>/dev/null | head -1 || true)
  if [[ -z "$PIXIE_DEPLOY_KEY" ]]; then
    PIXIE_DEPLOY_KEY=$(px deploy-key create -q)
  fi
  export PIXIE_DEPLOY_KEY
}

pixie_vizier_installed() {
  kubectl get ns "$PIXIE_NAMESPACE" >/dev/null 2>&1 \
    && kubectl get pods -n "$PIXIE_NAMESPACE" -l name=vizier-pem --no-headers 2>/dev/null | grep -q .
}

install_pixie_vizier() {
  if pixie_vizier_installed; then
    echo "==> Pixie Vizier already present in namespace ${PIXIE_NAMESPACE}"
    return 0
  fi
  ensure_pixie_auth
  ensure_pixie_deploy_key
  echo "==> Deploy Pixie Vizier to namespace ${PIXIE_NAMESPACE}"
  if command -v px >/dev/null 2>&1 && px deploy --help >/dev/null 2>&1; then
    px deploy --pem_memory_limit=1Gi --cluster_name="$PIXIE_CLUSTER_NAME" || {
      echo "WARN: px deploy failed — trying Helm fallback" >&2
      install_pixie_vizier_helm
    }
  else
    install_pixie_vizier_helm
  fi
}

install_pixie_vizier_helm() {
  command -v helm >/dev/null 2>&1 || {
    echo "ERROR: helm required when px deploy is unavailable or fails" >&2
    echo "       Install: curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash" >&2
    echo "       Or: sudo snap install helm --classic" >&2
    exit 1
  }
  ensure_pixie_deploy_key
  helm repo add pixie-operator https://pixie-operator-charts.storage.googleapis.com >/dev/null 2>&1 || true
  helm repo update pixie-operator >/dev/null
  helm upgrade --install pixie pixie-operator/pixie-operator-chart \
    --namespace "$PIXIE_NAMESPACE" --create-namespace \
    --set "deployKey=${PIXIE_DEPLOY_KEY}" \
    --set "clusterName=${PIXIE_CLUSTER_NAME}" \
    --set pemMemoryLimit=1Gi \
    --set dataAccess=Full
}

wait_pixie_vizier_ready() {
  local max_wait="${1:-300}" i=0
  echo "==> Wait for Pixie Vizier pods in ${PIXIE_NAMESPACE}"
  while [[ "$i" -lt "$max_wait" ]]; do
    local pem_ok conn_ok
    pem_ok=$(kubectl get pods -n "$PIXIE_NAMESPACE" -l name=vizier-pem --no-headers 2>/dev/null | awk '$3=="Running"{n++} END{print n+0}')
    conn_ok=$(kubectl get pods -n "$PIXIE_NAMESPACE" -l name=vizier-cloud-connector --no-headers 2>/dev/null | awk '$3=="Running"{n++} END{print n+0}')
    if [[ "${pem_ok:-0}" -ge 1 && "${conn_ok:-0}" -ge 1 ]]; then
      echo "    Vizier PEM + cloud-connector Running"
      return 0
    fi
    echo "    waiting (${i}s/${max_wait}s) pem=${pem_ok:-0} connector=${conn_ok:-0}"
    sleep 5
    i=$((i + 5))
  done
  echo "ERROR: Pixie Vizier not ready in ${PIXIE_NAMESPACE}" >&2
  kubectl get pods -n "$PIXIE_NAMESPACE" 2>&1 | sed 's/^/       /' >&2 || true
  return 1
}

wait_pixie_vizier_healthy() {
  local max_wait="${1:-120}" i=0
  echo "==> Wait for Pixie Vizier CS_HEALTHY (px can run queries)"
  while [[ "$i" -lt "$max_wait" ]]; do
    if px get viziers 2>/dev/null | grep -qE '[[:space:]]CS_HEALTHY([[:space:]]|$)'; then
      echo "    Vizier CS_HEALTHY"
      return 0
    fi
    echo "    waiting for CS_HEALTHY (${i}s/${max_wait}s)"
    sleep 5
    i=$((i + 5))
  done
  echo "ERROR: Pixie Vizier not CS_HEALTHY" >&2
  px get viziers 2>&1 | sed 's/^/       /' >&2 || true
  echo "       Recovery: kubectl delete pod -n pl vizier-metadata-0; kubectl delete pod -n pl -l name=vizier-pem" >&2
  return 1
}

pixie_vizier_healthy() {
  px get viziers 2>/dev/null | grep -qE '[[:space:]]CS_HEALTHY([[:space:]]|$)'
}

wait_pixie_http_events_ready() {
  local max_wait="${1:-180}" i=0 probe
  probe=$(mktemp)
  cat >"$probe" <<'EOF'
import px
df = px.DataFrame(table='http_events', start_time='-5s')
px.display(df.head(1))
EOF
  echo "==> Wait for Pixie http_events schema (post-PEM restart warm-up)"
  while [[ "$i" -lt "$max_wait" ]]; do
    if timeout 25 px run -f "$probe" >/dev/null 2>&1; then
      rm -f "$probe"
      echo "    http_events table ready"
      return 0
    fi
    echo "    waiting for http_events (${i}s/${max_wait}s)"
    sleep 5
    i=$((i + 5))
  done
  rm -f "$probe"
  echo "ERROR: Pixie http_events not ready — PEM may still be registering" >&2
  px get viziers 2>&1 | sed 's/^/       /' >&2 || true
  echo "       Recovery: kubectl delete pod -n pl vizier-metadata-0; kubectl delete pod -n pl -l name=vizier-pem" >&2
  return 1
}

pixie_bridge_repo() {
  if [[ -n "${REPO:-}" ]]; then
    echo "$REPO"
    return 0
  fi
  cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd
}

pixie_pxl_template() {
  local kind="${1:-ingress}" repo tpl marker
  repo=$(pixie_bridge_repo)
  tpl="${repo}/testing/scripts/manifests/pixie-bridge/configmap.yaml"
  case "$kind" in
    ingress) marker='http-ingress-export.pxl.tmpl' ;;
    egress) marker='http-egress-export.pxl.tmpl' ;;
    mongo) marker='mongodb-export.pxl.tmpl' ;;
    *) echo "ERROR: unknown PxL template kind: $kind" >&2; return 1 ;;
  esac
  if [[ -n "${PIXIE_PXL_TEMPLATE:-}" ]]; then
    cat "$PIXIE_PXL_TEMPLATE"
    return 0
  fi
  awk -v m="$marker" '$0 ~ m": \\|"{found=1;next} found && /^  [a-z]/ {exit} found{print}' "$tpl" | sed 's/^    //'
}

_render_pixie_pxl() {
  local rule_json="$1" out="$2" kind="$3"
  local labels excludes ports hosts label_lines exclude_lines port_lines host_lines tpl tmp_rule
  labels=$(echo "$rule_json" | jq -c '.spec.targetLabels // {}')
  excludes=$(echo "$rule_json" | jq -c '.spec.excludePaths // []')
  ports=$(echo "$rule_json" | jq -c '.spec.targetPorts // []')
  hosts=$(echo "$rule_json" | jq -c '.spec.recordAndReplayHosts // []')
  exclude_lines=$(pixie_exclude_path_lines "$excludes")
  if [[ "$kind" == "ingress" ]]; then
    label_lines=$(pixie_label_filter_lines "$labels")
    port_lines=$(pixie_port_filter_lines "$ports")
    host_lines="# ingress: prod pod label filters"
  else
    label_lines="# egress: Host allowlist (not prod pod labels)"
    port_lines="# egress: no local_port filter"
    host_lines=$(pixie_egress_host_filter_lines "$hosts")
  fi
  tpl=$(pixie_pxl_template "$kind")
  mkdir -p "$(dirname "$out")"
  tmp_rule=$(mktemp)
  printf '%s' "$rule_json" >"$tmp_rule"
  PIXL_TPL="$tpl" PIXL_KIND="$kind" PIXL_LABELS="$label_lines" PIXL_EXCLUDES="$exclude_lines" PIXL_PORTS="$port_lines" PIXL_EGRESS_HOSTS="$host_lines" \
    python3 - "$tmp_rule" "$out" <<'PY'
import json, sys, os

rule_path, out = sys.argv[1], sys.argv[2]
with open(rule_path) as f:
    rule = json.load(f)
spec = rule.get('spec', {})
tpl = os.environ['PIXL_TPL']
text = tpl.replace('__TARGET_NAMESPACE__', spec.get('targetNamespace', 'default'))
text = text.replace('__OTEL_ENDPOINT__', spec.get('otelEndpoint', ''))
text = text.replace('__RECORDER_OTEL_ENDPOINT__', spec.get('recorderOtelEndpoint', ''))
text = text.replace('__LABEL_FILTERS__', os.environ.get('PIXL_LABELS', '').strip())
text = text.replace('__EXCLUDE_PATH_FILTERS__', os.environ.get('PIXL_EXCLUDES', '').strip())
text = text.replace('__PORT_FILTERS__', os.environ.get('PIXL_PORTS', '').strip())
text = text.replace('__EGRESS_HOST_FILTERS__', os.environ.get('PIXL_EGRESS_HOSTS', '').strip())
with open(out, 'w') as f:
    f.write(text)
PY
  rm -f "$tmp_rule"
}

render_pixie_ingress_pxl() {
  _render_pixie_pxl "$1" "$2" ingress
}

render_pixie_egress_pxl() {
  _render_pixie_pxl "$1" "$2" egress
}

render_pixie_mongo_pxl() {
  local rule_json="$1" out="$2" tpl tmp_rule
  tpl=$(pixie_pxl_template mongo)
  mkdir -p "$(dirname "$out")"
  tmp_rule=$(mktemp)
  printf '%s' "$rule_json" >"$tmp_rule"
  PIXL_TPL="$tpl" python3 - "$tmp_rule" "$out" <<'PY'
import json, sys, os
rule_path, out = sys.argv[1], sys.argv[2]
with open(rule_path) as f:
    rule = json.load(f)
spec = rule.get('spec', {})
tpl = os.environ['PIXL_TPL']
text = tpl.replace('__SHADOW_NAMESPACE__', spec.get('shadowNamespace', ''))
text = text.replace('__MONGO_OTEL_ENDPOINT__', spec.get('mongoOtelEndpoint', ''))
with open(out, 'w') as f:
    f.write(text)
PY
  rm -f "$tmp_rule"
}

# ponytail: legacy name — ingress export only
render_pixie_export_pxl() {
  render_pixie_ingress_pxl "$1" "$2"
}

pixie_label_filter_lines() {
  local labels_json="$1"
  if [[ -z "$labels_json" || "$labels_json" == "null" ]]; then
    echo "# no label filters"
    return 0
  fi
  local keys
  keys=$(echo "$labels_json" | jq -r 'keys[]' 2>/dev/null || true)
  local k v line
  for k in $keys; do
    v=$(echo "$labels_json" | jq -r --arg k "$k" '.[$k]')
    # ponytail: pod name contains app label value (service ctx is often empty on minikube)
    if [[ "$k" == "app" ]]; then
      line="df = df[px.contains(df.pod, '${v}')]"
    else
      line="df = df[df.ctx['${k}'] == '${v}']"
    fi
    echo "$line"
  done
}

pixie_exclude_path_lines() {
  local paths_json="$1"
  if [[ -z "$paths_json" || "$paths_json" == "null" || "$paths_json" == "[]" ]]; then
    echo "# no exclude paths"
    return 0
  fi
  local n i re
  n=$(echo "$paths_json" | jq 'length')
  i=0
  while [[ "$i" -lt "$n" ]]; do
    re=$(echo "$paths_json" | jq -r ".[$i]")
    echo "df = df[not px.regex_match('${re}', df.req_path)]"
    i=$((i + 1))
  done
}

pixie_port_filter_lines() {
  local ports_json="$1" p
  if [[ -z "$ports_json" || "$ports_json" == "null" || "$ports_json" == "[]" ]]; then
    echo "# no port filters"
    return 0
  fi
  # ponytail: PxL has no vectorized OR (| or or); filter lowest port (prod app :80, not Envoy :8888)
  p=$(echo "$ports_json" | jq -r 'min')
  echo "df = df[df.local_port == ${p}]"
}

pixie_egress_host_filter_lines() {
  local hosts_json="$1" n i host re
  if [[ -z "$hosts_json" || "$hosts_json" == "null" || "$hosts_json" == "[]" ]]; then
    echo "# no egress host filters"
    return 0
  fi
  n=$(echo "$hosts_json" | jq 'length')
  if [[ "$n" -eq 1 ]]; then
    host=$(echo "$hosts_json" | jq -r '.[0]')
    echo "df = df[df.req_host == '${host}']"
    return 0
  fi
  # ponytail: PxL has no vectorized OR; regex union for multiple allowlist hosts
  re=""
  i=0
  while [[ "$i" -lt "$n" ]]; do
    host=$(echo "$hosts_json" | jq -r ".[$i]" | sed 's/[.]/\\./g')
    re="${re}${re:+|}${host}"
    i=$((i + 1))
  done
  echo "df = df[px.regex_match('^(${re})\$', df.req_host)]"
}

patch_pixie_stream_rule_status() {
  local ns="$1" name="$2" phase="$3" msg="${4:-}"
  kubectl patch pixiestreamrule "$name" -n "$ns" --type=merge --subresource=status \
    -p "{\"status\":{\"phase\":\"${phase}\",\"message\":\"${msg}\"}}" 2>/dev/null || true
}

apply_pixie_bridge_manifests() {
  local repo
  repo=$(pixie_bridge_repo)
  kubectl create namespace monarch-system --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl apply -k "${repo}/testing/scripts/manifests/pixie-bridge/"
  echo "    applied pixie-stream-bridge RBAC + ConfigMap in monarch-system"
}

run_pixie_export_once() {
  local pxl_file="$1" err
  # ponytail: px run blocks on OTLP export; timeout keeps bridge killable via SIGTERM
  if ! err=$(timeout 25 px run -f "$pxl_file" 2>&1); then
    echo "WARN: px run export failed for ${pxl_file}: $(echo "$err" | tail -1)" >&2
    return 1
  fi
}

pixie_bridge_start_hint() {
  local repo
  repo=$(pixie_bridge_repo)
  echo "${repo}/testing/scripts/start-pixie-stream-bridge.sh"
}

# mkdir before nohup redirect — shell opens bridge.log before pixie-stream-bridge.sh runs.
start_pixie_stream_bridge_background() {
  local repo pid_file
  repo=$(pixie_bridge_repo)
  export REPO="$repo"
  PIXIE_BRIDGE_STATE_DIR="${PIXIE_BRIDGE_STATE_DIR:-${repo}/.cache/pixie-bridge}"
  mkdir -p "$PIXIE_BRIDGE_STATE_DIR"
  pid_file="${PIXIE_BRIDGE_STATE_DIR}/bridge.pid"
  local existing_pid=""
  if [[ -f "$pid_file" ]]; then
    existing_pid=$(cat "$pid_file" 2>/dev/null || true)
  fi
  # Verify the PID is actually our bridge script, not a coincidentally-reused PID.
  if [[ -n "$existing_pid" ]] && kill -0 "$existing_pid" 2>/dev/null && \
     grep -q "pixie-stream-bridge" "/proc/${existing_pid}/cmdline" 2>/dev/null; then
    echo "pixie-stream-bridge already running pid=${existing_pid}"
    return 0
  fi
  nohup "${repo}/testing/scripts/pixie-stream-bridge.sh" >"${PIXIE_BRIDGE_STATE_DIR}/bridge.log" 2>&1 &
  echo $! >"$pid_file"
  echo "pixie-stream-bridge started pid=$(cat "$pid_file") log=${PIXIE_BRIDGE_STATE_DIR}/bridge.log"
}
