# Cluster-agnostic Monarch E2E deploy/wait/test tail.
# Source after provider bootstrap (Kind load or Minikube docker-env builds).
# Config via env only: MONARCH_IMG, SHADOWTEST, SKIP_LOAD, RUN_*, E2E_IMAGE_REBUILD_HINT, etc.

e2e_reset_deploy_stack() {
  echo "==> Monarch CRDs"
  make -C pipeline/monarch install

  if [[ "${NO_RESET:-0}" -eq 0 ]]; then
    echo "==> Delete prior E2E resources (if any)"
    kubectl delete shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" --ignore-not-found --wait=false
    kubectl delete deployment,service my-prod-app -n default --ignore-not-found --wait=false
    # Headless vs ClusterIP is immutable; recreate so record-replay dial-by-endpoint works.
    kubectl delete service egress-httpbin -n default --ignore-not-found --wait=false
    wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
  fi

  echo "==> Monarch operator"
  make -C pipeline/monarch deploy IMG="$MONARCH_IMG"
  kubectl set env deployment/monarch-controller-manager -n monarch-system \
    MONARCH_MODE=dev
  # Same :dev tag does not change Deployment spec after image rebuild; restart picks up new layers.
  if [[ "${SKIP_LOAD:-0}" -eq 0 ]]; then
    echo "==> Restart Monarch manager (pick up re-loaded ${MONARCH_IMG})"
    kubectl rollout restart deployment/monarch-controller-manager -n monarch-system
  fi
  kubectl rollout status deployment/monarch-controller-manager -n monarch-system --timeout=180s

  echo "==> Beru"
  kubectl apply -f pipeline/beru/deploy/
  kubectl set image deployment/beru beru="$BERU_IMG" -n beru-system
  kubectl rollout status deployment/beru -n beru-system --timeout=120s

  echo "==> Siphon RBAC (per-shadow OTLP receiver; Monarch writes PixieStreamRule)"
  kubectl apply -f pipeline/siphon/deploy/rbac.yaml

  echo "==> Production app (echo on :80, memory limits)"
  kubectl apply -f testing/scripts/manifests/e2e-prod-app.yaml
  kubectl apply -f testing/scripts/manifests/e2e-egress-httpbin.yaml
  kubectl wait -n default --for=condition=Available deployment/egress-httpbin --timeout=120s
  kubectl rollout status deployment/my-prod-app -n default --timeout=120s
  kubectl wait -n default --for=condition=Ready pod -l app=my-prod-app --timeout=120s

  echo "==> ShadowTest (servicePort=8888, applicationPort=80, Igris :80/:8888, recordAndReplay for egress recorder)"
  wait_shadowtest_gone "$SHADOWTEST" "$SHADOWTEST_NS" 180
  kubectl apply -f testing/scripts/manifests/e2e-shadowtest.yaml
  if ! kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" >/dev/null 2>&1; then
    echo "ERROR: ShadowTest $SHADOWTEST_NS/$SHADOWTEST missing after apply" >&2
    exit 1
  fi
  nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"

  echo "==> Wait for ShadowTest Ready (Monarch reconciles PixieStreamRule + shadow Siphon Service)"
  local i phase siphon message shadow_ns
  for i in $(seq 1 36); do
    phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    siphon=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}' 2>/dev/null || true)
    message=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.message}' 2>/dev/null || true)
    shadow_ns=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}' 2>/dev/null || true)
    if [[ "$phase" == "Ready" && "$siphon" == "Ready" ]]; then
      break
    fi
    if [[ "$phase" == "Ready" && "$i" -ge 6 ]]; then
      nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"
    fi
    echo "    phase=$phase siphon=$siphon msg=${message:-<none>} (${i}/36)"
    sleep 5
  done

  kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o custom-columns=\
PHASE:.status.phase,SIPHON:.status.siphonPhase,NS:.status.shadowNamespace,CAPTURE:.status.captureTargets

  SHADOW_NS=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.shadowNamespace}')
  phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.phase}')
  siphon_phase=$(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.siphonPhase}')
  if [[ "$phase" != "Ready" ]]; then
    echo "ERROR: ShadowTest not Ready — check: kubectl describe shadowtest $SHADOWTEST -n $SHADOWTEST_NS" >&2
    echo "       If Envoy sidecars CrashLoopBackOff: kubectl logs -n \$SHADOW_NS deploy/${SHADOWTEST}-control-a -c envoy-sidecar --tail=20" >&2
    if [[ -n "${E2E_IMAGE_REBUILD_HINT:-}" ]]; then
      echo "       ${E2E_IMAGE_REBUILD_HINT}" >&2
    fi
    exit 1
  fi
  if [[ "$siphon_phase" == "Degraded" ]]; then
    echo "WARN: siphonPhase=Degraded — check PixieStreamRule and monarch-controller logs"
  fi

  if [[ -n "${SHADOW_NS:-}" && "$siphon_phase" == "Ready" ]]; then
    echo "==> Shadow Siphon OTLP receiver (Pixie export destination)"
    wait_pixie_capture_ready "$SHADOWTEST" "$SHADOWTEST_NS" "$SHADOW_NS" 120
    wait_shadow_siphon_otlp "$SHADOW_NS"
  fi

  echo "==> Wait for Recorder rollout (Monarch resolves recorder:dev via MONARCH_MODE=dev)"
  if [[ -n "${SHADOW_NS:-}" ]]; then
    wait_recorder_rollout "$SHADOWTEST" "$SHADOWTEST_NS" "$SHADOW_NS" "$RECORDER_IMG" 120s
  fi

  echo "==> Nudge Monarch reconcile (recorder_host after Recorder is up)"
  nudge_siphon_config "$SHADOWTEST" "$SHADOWTEST_NS"
  sleep 3

  echo "==> Verify shadow Envoy -> app port (applicationPort=80)"
  local role deploy app_port
  for role in control-a control-b candidate; do
    deploy="${SHADOWTEST}-${role}"
    app_port=$(kubectl get deploy "$deploy" -n "$SHADOW_NS" -o jsonpath='{.spec.template.spec.containers[?(@.name=="app")].ports[0].containerPort}')
    echo "    $deploy app containerPort=$app_port"
  done

  echo ""
  echo "E2E stack is up."
  echo "  Shadow namespace: $SHADOW_NS"
  local prod_ip
  prod_ip=$(kubectl get pods -n default -l app=my-prod-app -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}' 2>/dev/null | head -1)
  echo "  Prod IP:          ${prod_ip:-<pending>}"
  echo "  Capture labels:   $(kubectl get shadowtest "$SHADOWTEST" -n "$SHADOWTEST_NS" -o jsonpath='{.status.captureTargets}' 2>/dev/null || echo '<pending>')"
  echo "  Pixie export:     siphon.${SHADOW_NS}.svc.cluster.local:4317"
  echo "  (ingress capture requires Pixie — ./testing/scripts/setup-local-pixie.sh)"
  echo ""
  echo "Run ingress test:  ./testing/scripts/e2e-pipeline-test.sh"
  echo "Run egress test:   ./testing/scripts/e2e-egress-test.sh"
  echo "Run record-replay: ./testing/scripts/e2e-record-replay.sh"
  echo "Run dependency E2E: ./testing/scripts/e2e-dependency-test.sh"
  echo "Run RabbitMQ E2E:  ./testing/scripts/e2e-rabbitmq-test.sh"
  echo "Run OTel RabbitMQ E2E: ./testing/scripts/e2e-otel-rabbitmq-test.sh"
  echo "Run OTLP ingress E2E: ./testing/scripts/e2e-siphon-otlp-ingress-test.sh"
  echo "Run Pixie egress record E2E: ./testing/scripts/e2e-pixie-egress-record-test.sh"
  echo "Run k6 stress:     testing/example-apps/k6/run-stress-test.sh  (or see testing/example-apps/k6/README.md)"

  if [[ "${RUN_TEST:-0}" -eq 1 ]]; then
    echo ""
    ./testing/scripts/e2e-pipeline-test.sh
  fi

  if [[ "${RUN_EGRESS_TEST:-0}" -eq 1 ]]; then
    echo ""
    ./testing/scripts/e2e-egress-test.sh
  fi

  if [[ "${RUN_RECORD_REPLAY:-0}" -eq 1 ]]; then
    echo ""
    chmod +x testing/scripts/e2e-record-replay.sh
    ./testing/scripts/e2e-record-replay.sh
  fi

  if [[ "${RUN_DEPENDENCY_TEST:-0}" -eq 1 ]]; then
    echo ""
    chmod +x testing/scripts/e2e-dependency-test.sh
    ./testing/scripts/e2e-dependency-test.sh
  fi

  if [[ "${RUN_RABBITMQ_TEST:-0}" -eq 1 ]]; then
    echo ""
    chmod +x testing/scripts/e2e-rabbitmq-test.sh
    ./testing/scripts/e2e-rabbitmq-test.sh
  fi

  if [[ "${RUN_OTEL_RABBITMQ_TEST:-0}" -eq 1 ]]; then
    echo ""
    chmod +x testing/scripts/e2e-otel-rabbitmq-test.sh
    ./testing/scripts/e2e-otel-rabbitmq-test.sh
  fi

  if [[ "${RUN_OTLP_INGRESS_TEST:-0}" -eq 1 ]]; then
    echo ""
    chmod +x testing/scripts/e2e-siphon-otlp-ingress-test.sh
    ./testing/scripts/e2e-siphon-otlp-ingress-test.sh
  fi
}
