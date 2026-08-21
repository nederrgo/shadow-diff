# Shared E2E stack deploy for e2e-reset-{minikube,kind}.sh.
# Source after cluster helpers + image build/load. Do not execute directly.
#
# Required env (set by the driver):
#   REPO, SHADOWTEST, SHADOWTEST_NS, MONARCH_IMG, BERU_IMG, SHOP_IMG, IGRIS_IMG,
#   IGRIS_RABBITMQ_IMG, EGRESS_RELAY_RABBITMQ_IMG, SHADOW_SOLDIER_IMG, KAISEL_IMG,
#   TUSK_IMG, THE_SYSTEM_IMG
# Optional:
#   NO_RESET, SKIP_LOAD, E2E_IMAGE_REBUILD_HINT
#   E2E_HOST_PG_ADDR — host:port for Postgres NodePort (Kind: localhost:15432;
#     Minikube: $(minikube ip):30432). Defaults to localhost:15432 if unset.

e2e_reset_deploy_stack() {
  echo "==> Monarch CRDs (ShadowTest + KaiselRule)"
  make -C pipeline/monarch install

  if [[ "${NO_RESET:-0}" -eq 0 ]]; then
    echo "==> Delete prior E2E resources (if any)"
    kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=false
    kubectl delete deployment,service my-prod-app -n default --ignore-not-found --wait=false
    kubectl delete service egress-httpbin -n default --ignore-not-found --wait=false 2>/dev/null || true
    wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
  fi

  echo "==> Monarch operator"
  make -C pipeline/monarch deploy IMG="$MONARCH_IMG"

  echo "==> Secret-source RoleBinding (credentialsSecretRef in default)"
  kubectl apply -f "$REPO/testing/bats/manifests/monarch-secret-source-rbac.yaml"

  # Local PostgreSQL fixture for Beru's durable backend (BYO-Postgres). Not
  # managed by Monarch. Applied here because `make deploy` creates monarch-system,
  # and the Secret must exist before the manager reconciles a ShadowTest.
  echo "==> PostgreSQL (monarch-system, BYO-Postgres local fixture)"
  kubectl apply -f "$REPO/testing/bats/manifests/postgres/deployment.yaml"
  kubectl apply -f "$REPO/testing/bats/manifests/postgres/service.yaml"
  kubectl rollout status deployment/postgres -n monarch-system --timeout=180s

  # BERU_DB_SECRET: Monarch replicates the Secret into each shadow namespace;
  # beru-local mounts it via envFrom and keeps a disk WAL at /data.
  echo "    beru-local storage: postgres (BERU_DB_SECRET=monarch-system/beru-postgres)"
  # Env overrides keep bare local tags; unset helpers would default to ghcr.io/shadow-diff/*.
  manager_env=(
    MONARCH_MODE=dev
    BERU_IMAGE="$BERU_IMG"
    SHOP_IMAGE="$SHOP_IMG"
    IGRIS_HTTP_IMAGE="$IGRIS_IMG"
    IGRIS_RABBITMQ_IMAGE="$IGRIS_RABBITMQ_IMG"
    EGRESS_RELAY_RABBITMQ_IMAGE="$EGRESS_RELAY_RABBITMQ_IMG"
    SHADOW_SOLDIER_IMAGE="$SHADOW_SOLDIER_IMG"
    BERU_DB_SECRET=monarch-system/beru-postgres
  )
  kubectl set env deployment/monarch-controller-manager -n monarch-system "${manager_env[@]}"

  if [[ "${SKIP_LOAD:-0}" -eq 0 ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s

  # Local MinIO fixture for ShadowTest.spec.storage (BYOB). Not managed by Monarch.
  echo "==> MinIO (monarch-system, BYOB local fixture)"
  kubectl apply -f "$REPO/testing/bats/manifests/minio/deployment.yaml"
  kubectl apply -f "$REPO/testing/bats/manifests/minio/service.yaml"
  kubectl apply -f "$REPO/testing/bats/manifests/minio/credentials-secret.yaml"
  kubectl rollout status deployment/minio -n monarch-system --timeout=180s
  # Job name is fixed; delete any prior run so apply can recreate.
  kubectl delete job minio-create-bucket -n monarch-system --ignore-not-found --wait=true
  kubectl apply -f "$REPO/testing/bats/manifests/minio/bucket-job.yaml"
  kubectl wait --for=condition=complete job/minio-create-bucket -n monarch-system --timeout=120s

  # Kaisel: cluster-wide HTTP ingress capture (Monarch writes KaiselRule per ShadowTest).
  # shellcheck source=testing/bats/lib/kaisel.bash
  source "$REPO/testing/bats/lib/kaisel.bash"
  echo "==> Kaisel DaemonSet (kaisel-system image=${KAISEL_IMG})"
  kaisel_daemonset_deploy
  kaisel_daemonset_wait_ready 120

  # Tusk: cluster-wide BFF — Monarch :9090 → topology WS, and shared Postgres
  # (beru-postgres Secret via envFrom) → ShadowDiff REST/WS for The System.
  echo "==> Tusk BFF (monarch-system image=${TUSK_IMG})"
  if ! kubectl get secret beru-postgres -n monarch-system >/dev/null 2>&1; then
    echo "ERROR: secret/beru-postgres missing in monarch-system (required for Tusk ShadowDiff)" >&2
    exit 1
  fi
  kubectl apply -k "$REPO/pipeline/tusk/deploy"
  kubectl set image deployment/tusk -n monarch-system "tusk=${TUSK_IMG}"
  kubectl rollout status deployment/tusk -n monarch-system --timeout=120s

  # The System: cluster-wide topology / ShadowTest editor UI (Nginx :80).
  echo "==> The System UI (monarch-system image=${THE_SYSTEM_IMG})"
  kubectl apply -k "$REPO/pipeline/the-system/deploy"
  kubectl set image deployment/the-system -n monarch-system "the-system=${THE_SYSTEM_IMG}"
  kubectl rollout status deployment/the-system -n monarch-system --timeout=120s

  echo "==> Production app (echo on :80, memory limits)"
  kubectl apply -f "$REPO/testing/bats/manifests/e2e-prod-app.yaml"
  kubectl rollout status deployment/my-prod-app -n default --timeout=120s
  kubectl wait -n default --for=condition=Ready pod -l app=my-prod-app --timeout=120s

  echo "==> ShadowTest (servicePort=8888, applicationPort=80, Igris :80/:8888; Shop always-on)"
  wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
  kubectl apply -f "$REPO/testing/bats/manifests/e2e-shadowtest.yaml"
  if ! kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
    echo "ERROR: ShadowTest $SHADOWTEST_NS/$SHADOWTEST missing after apply" >&2
    exit 1
  fi

  echo "==> Wait for ShadowTest Ready (Monarch reconciles KaiselRule)"
  local i phase kaisel message shadow_ns
  for i in $(seq 1 36); do
    phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    kaisel=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.kaiselPhase}' 2>/dev/null || true)
    message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    if [[ "$phase" == "Ready" && "$kaisel" == "Ready" ]]; then
      break
    fi
    echo "    phase=$phase kaisel=$kaisel msg=${message:-<none>} (${i}/36)"
    sleep 5
  done

  kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o custom-columns=\
PHASE:.status.phase,KAISEL:.status.kaiselPhase,NS:.status.shadowNamespace,CAPTURE:.status.captureTargets

  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}')
  kaisel_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.kaiselPhase}')
  if [[ "$phase" != "Ready" ]]; then
    echo "ERROR: ShadowTest not Ready — check: kubectl describe shadowtest $SHADOWTEST -n $SHADOWTEST_NS" >&2
    if [[ -n "${E2E_IMAGE_REBUILD_HINT:-}" ]]; then
      echo "       ${E2E_IMAGE_REBUILD_HINT}" >&2
    fi
    exit 1
  fi
  if [[ "$kaisel_phase" == "Degraded" ]]; then
    echo "WARN: kaiselPhase=Degraded — check KaiselRule and monarch-controller logs"
  fi

  if [[ -n "${SHADOW_NS:-}" ]]; then
    wait_local_beru_rollout "$SHADOW_NS"
  fi

  local host_pg="${E2E_HOST_PG_ADDR:-localhost:15432}"

  echo ""
  echo "E2E stack is up."
  echo "  Shadow namespace: $SHADOW_NS"
  local prod_ip
  prod_ip=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}' 2>/dev/null | head -1)
  echo "  Prod IP:          ${prod_ip:-<pending>}"
  echo "  Capture labels:   $(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.captureTargets}' 2>/dev/null || echo '<pending>')"
  echo "  Kaisel DaemonSet: kaisel-system (image ${KAISEL_IMG})"
  echo "  Kaisel ingress:   KaiselRule kaisel-${SHADOWTEST} -> igris in ${SHADOW_NS}"
  echo "  Beru (local):     beru-local.${SHADOW_NS}.svc.cluster.local:50051"
  echo "  MinIO (local):    minio-service.monarch-system.svc.cluster.local:9000 (bucket shadow-diff-local)"
  echo "  Postgres (local): postgres.monarch-system.svc.cluster.local:5432 (db/user/pass: beru)"
  echo "    from host:      postgres://beru:beru@${host_pg}/beru?sslmode=disable"
  echo ""
  echo "Run bats tests:     make test-bats-e2e"
  echo "  Kaisel route E2E: make test-bats-kaisel"
}
