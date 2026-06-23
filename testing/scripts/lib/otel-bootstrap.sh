#!/usr/bin/env bash
# Install cert-manager and OpenTelemetry Operator on the current kubectl context.
# shellcheck disable=SC1091
set -euo pipefail

CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.12.0}"
OTEL_OPERATOR_VERSION="${OTEL_OPERATOR_VERSION:-v0.80.0}"
CERT_MANAGER_MANIFEST="https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
OTEL_OPERATOR_MANIFEST="https://github.com/open-telemetry/opentelemetry-operator/releases/download/${OTEL_OPERATOR_VERSION}/opentelemetry-operator.yaml"
OTEL_OPERATOR_IMAGE="${OTEL_OPERATOR_IMAGE:-ghcr.io/open-telemetry/opentelemetry-operator/opentelemetry-operator:${OTEL_OPERATOR_VERSION#v}}"
KUBE_RBAC_PROXY_IMAGE="${KUBE_RBAC_PROXY_IMAGE:-gcr.io/kubebuilder/kube-rbac-proxy:v0.13.1}"
# gcr.io/kubebuilder/kube-rbac-proxy is deprecated; pull from quay and retag for Kind preload.
KUBE_RBAC_PROXY_PULL_IMAGE="${KUBE_RBAC_PROXY_PULL_IMAGE:-quay.io/brancz/kube-rbac-proxy:v0.13.1}"

_kind_cluster_name() {
  if [[ -n "${KIND_CLUSTER:-}" ]]; then
    echo "$KIND_CLUSTER"
    return
  fi
  local ctx
  ctx=$(kubectl config current-context 2>/dev/null || true)
  if [[ "$ctx" == kind-* ]]; then
    echo "${ctx#kind-}"
    return
  fi
  local name
  for name in $(kind get clusters 2>/dev/null); do
    if kind get nodes --name "$name" >/dev/null 2>&1; then
      echo "$name"
      return
    fi
  done
}

_otel_bootstrap_repo() {
  if [[ -n "${REPO:-}" ]]; then
    echo "$REPO"
    return
  fi
  cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd
}

load_otel_operator_images() {
  if [[ "${SKIP_OTEL_IMAGE_PRELOAD:-0}" == "1" ]]; then
    echo "==> Skipping OTel operator image preload (--skip or SKIP_OTEL_IMAGE_PRELOAD=1)"
    return 0
  fi
  command -v docker >/dev/null 2>&1 || return 0

  local ctx repo docker_sh
  ctx=$(kubectl config current-context 2>/dev/null || true)

  repo="$(_otel_bootstrap_repo)"
  docker_sh="$repo/testing/scripts/lib/docker.sh"

  echo "==> Preload OTel operator images (context=${ctx:-<unknown>})"
  echo "    ${OTEL_OPERATOR_IMAGE}"
  echo "    ${KUBE_RBAC_PROXY_IMAGE} (pull ${KUBE_RBAC_PROXY_PULL_IMAGE})"
  bash "$docker_sh" pull "$OTEL_OPERATOR_IMAGE"
  bash "$docker_sh" pull "$KUBE_RBAC_PROXY_PULL_IMAGE"
  if [[ "$KUBE_RBAC_PROXY_PULL_IMAGE" != "$KUBE_RBAC_PROXY_IMAGE" ]]; then
    docker tag "$KUBE_RBAC_PROXY_PULL_IMAGE" "$KUBE_RBAC_PROXY_IMAGE"
  fi

  if [[ "$ctx" == minikube ]]; then
    if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
      # shellcheck source=testing/scripts/lib/cluster-minikube.sh
      load_minikube_images "$OTEL_OPERATOR_IMAGE" "$KUBE_RBAC_PROXY_IMAGE"
    else
      echo "    images loaded into Minikube docker (eval minikube docker-env must be active)"
    fi
    return 0
  fi

  if [[ "$ctx" == kind-* ]]; then
    command -v kind >/dev/null 2>&1 || return 0
    local kind_cluster="${ctx#kind-}"
    if [[ -n "${KIND_CLUSTER:-}" ]]; then
      kind_cluster="$KIND_CLUSTER"
    fi
    if ! kind get nodes --name "$kind_cluster" >/dev/null 2>&1; then
      echo "WARN: Kind cluster '${kind_cluster}' has no nodes; skipping kind load (set KIND_CLUSTER=...)" >&2
      return 0
    fi
    kind load docker-image "$OTEL_OPERATOR_IMAGE" --name "$kind_cluster"
    kind load docker-image "$KUBE_RBAC_PROXY_IMAGE" --name "$kind_cluster"
    return 0
  fi

  echo "WARN: unknown kubectl context '${ctx}'; OTel images pulled to host docker only" >&2
}

# ponytail: alias for callers not yet updated
load_otel_operator_images_into_kind() {
  load_otel_operator_images
}

recover_otel_operator_image_pull() {
  if kubectl get pods -n opentelemetry-operator-system --no-headers 2>/dev/null \
    | grep -q 'ImagePullBackOff\|ErrImagePull'; then
    echo "==> Detected ImagePullBackOff — loading images into Kind and restarting operator"
    load_otel_operator_images
    kubectl rollout restart deployment/opentelemetry-operator-controller-manager \
      -n opentelemetry-operator-system >/dev/null 2>&1 || true
    kubectl rollout status deployment/opentelemetry-operator-controller-manager \
      -n opentelemetry-operator-system --timeout=120s 2>/dev/null || true
    return 0
  fi
  return 1
}

cert_manager_ready() {
  kubectl get namespace cert-manager >/dev/null 2>&1 || return 1
  local ready total
  ready=$(kubectl get pods -n cert-manager --no-headers 2>/dev/null | awk '$3=="Running" && $2 ~ /^[0-9]+\/[0-9]+$/ {split($2,a,"/"); if(a[1]==a[2]) c++} END{print c+0}')
  total=$(kubectl get pods -n cert-manager --no-headers 2>/dev/null | wc -l | tr -d ' ')
  [[ "${total:-0}" -gt 0 && "${ready:-0}" -eq "${total}" ]]
}

otel_operator_ready() {
  kubectl get namespace opentelemetry-operator-system >/dev/null 2>&1 || return 1
  kubectl wait --namespace opentelemetry-operator-system \
    --for=condition=Available deployment/opentelemetry-operator-controller-manager \
    --timeout=5s >/dev/null 2>&1
}

install_cert_manager() {
  if cert_manager_ready; then
    echo "==> cert-manager already installed and ready"
    return 0
  fi

  echo "==> Install cert-manager (${CERT_MANAGER_VERSION})"
  kubectl apply -f "$CERT_MANAGER_MANIFEST"

  echo "==> Wait for cert-manager pods"
  kubectl wait --namespace cert-manager --for=condition=ready pod --all --timeout=120s

  echo "==> Warm-up window for cert-manager webhook CA propagation"
  sleep 15
}

wait_otel_serving_certificate() {
  if ! kubectl get certificate -n opentelemetry-operator-system opentelemetry-operator-serving-cert >/dev/null 2>&1; then
    return 0
  fi
  echo "==> Wait for OTel operator TLS certificate (cert-manager)"
  if ! kubectl wait --namespace opentelemetry-operator-system \
    --for=condition=Ready certificate/opentelemetry-operator-serving-cert \
    --timeout="${OTEL_CERT_WAIT_TIMEOUT:-180s}"; then
    echo "WARN: OTel serving certificate not Ready yet — continuing (see: kubectl describe certificate -n opentelemetry-operator-system opentelemetry-operator-serving-cert)" >&2
  fi
  echo "==> Warm-up window for OTel webhook CA propagation"
  sleep "${OTEL_WEBHOOK_WARMUP_SEC:-15}"
}

wait_otel_operator_deployment() {
  local max_wait="${OTEL_OPERATOR_WAIT_SEC:-300}"
  local interval=10
  local elapsed=0
  local image_pull_recovery=0

  echo "==> Wait for OpenTelemetry Operator deployment (up to ${max_wait}s)"
  while [[ "$elapsed" -lt "$max_wait" ]]; do
    if otel_operator_ready; then
      echo "    OpenTelemetry Operator is Available (${elapsed}s)"
      return 0
    fi

    local pod_line avail
    pod_line=$(kubectl get pods -n opentelemetry-operator-system -l app.kubernetes.io/name=opentelemetry-operator \
      --no-headers 2>/dev/null | head -1 || true)
    avail=$(kubectl get deploy -n opentelemetry-operator-system opentelemetry-operator-controller-manager \
      -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo "0")
    echo "    waiting (${elapsed}s/${max_wait}s) available=${avail:-0} pod=${pod_line:-<pending>}"

    if [[ "$pod_line" == *ImagePullBackOff* || "$pod_line" == *ErrImagePull* ]]; then
      if [[ "$image_pull_recovery" -eq 0 ]]; then
        recover_otel_operator_image_pull
        image_pull_recovery=1
        continue
      fi
      echo "ERROR: OTel operator pod still cannot pull images." >&2
      echo "       Preload manually, then restart the deployment:" >&2
      local ctx_hint
      ctx_hint=$(kubectl config current-context 2>/dev/null || true)
      if [[ "$ctx_hint" == minikube ]]; then
        echo "         eval \$(minikube docker-env)" >&2
        echo "         docker pull ${OTEL_OPERATOR_IMAGE}" >&2
        echo "         docker pull ${KUBE_RBAC_PROXY_PULL_IMAGE}" >&2
        echo "         docker tag ${KUBE_RBAC_PROXY_PULL_IMAGE} ${KUBE_RBAC_PROXY_IMAGE}" >&2
      else
        echo "         docker pull ${OTEL_OPERATOR_IMAGE}" >&2
        echo "         docker pull ${KUBE_RBAC_PROXY_IMAGE}" >&2
        echo "         kind load docker-image ${OTEL_OPERATOR_IMAGE} --name \${KIND_CLUSTER}" >&2
        echo "         kind load docker-image ${KUBE_RBAC_PROXY_IMAGE} --name \${KIND_CLUSTER}" >&2
      fi
      echo "         kubectl rollout restart deployment/opentelemetry-operator-controller-manager -n opentelemetry-operator-system" >&2
      kubectl describe pod -n opentelemetry-operator-system -l app.kubernetes.io/name=opentelemetry-operator 2>&1 | tail -25 >&2 || true
      return 1
    fi

    sleep "$interval"
    elapsed=$((elapsed + interval))
  done

  echo "ERROR: OpenTelemetry Operator did not become Available within ${max_wait}s" >&2
  kubectl get pods -n opentelemetry-operator-system -o wide 2>&1 || true
  kubectl describe deploy -n opentelemetry-operator-system opentelemetry-operator-controller-manager 2>&1 | tail -30 >&2 || true
  kubectl get certificate -n opentelemetry-operator-system 2>&1 || true
  return 1
}

install_otel_operator() {
  install_cert_manager

  if otel_operator_ready; then
    echo "==> OpenTelemetry Operator already installed and ready"
    return 0
  fi

  echo "==> Install OpenTelemetry Operator (${OTEL_OPERATOR_VERSION})"
  load_otel_operator_images
  kubectl apply -f "$OTEL_OPERATOR_MANIFEST"

  wait_otel_serving_certificate
  wait_otel_operator_deployment
}

install_otel_stack() {
  install_otel_operator
  echo "==> OTel stack ready (cert-manager + OpenTelemetry Operator)"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  install_otel_stack
fi
