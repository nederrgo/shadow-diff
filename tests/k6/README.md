# k6 stress test (Phase 4b.2)

Parallel load test for the shadow stack: steady JSON traffic, noisy payloads (Beru noise-filter stress), oversized bodies (Igris 413 guard), single-role orphan traces (Beru TTL timeouts), and continuous Beru `/healthz` probes with a **100% success threshold** for CI.

## Install k6

- macOS: `brew install k6`
- Debian/Ubuntu: `sudo gpg -k && sudo gpg --no-default-keyring --keyring /usr/share/keyrings/k6-archive-keyring.gpg --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69 && echo "deb [signed-by=/usr/share/keyrings/k6-archive-keyring.gpg] https://dl.k6.io/deb stable main" | sudo tee /etc/apt/sources.list.d/k6.list && sudo apt update && sudo apt install k6`
- Other platforms: [k6 installation docs](https://k6.io/docs/get-started/installation/)

## Prerequisites

1. Kind E2E stack is **Ready** (Monarch, Beru, Igris, shadows, optional Siphon):

   ```bash
   ./scripts/e2e-reset-kind.sh
   ```

2. Rebuild/load **Beru** with `GET /healthz` after pulling this branch. **Building alone does not update the running pod:**

   ```bash
   make beru-docker-build BERU_IMG=beru:dev
   kind load docker-image beru:dev --name "$(kind get clusters | head -1)"
   kubectl rollout restart deployment/beru -n beru-system
   kubectl rollout status deployment/beru -n beru-system
   curl -sf http://127.0.0.1:8080/healthz   # after port-forward to Beru :8080
   ```

   If `/healthz` returns **404**, the cluster still has the pre-healthz image — repeat the steps above (especially `kind load` + `rollout restart`).

## Port-forwards

In separate terminals (or background), forward Igris, control-a (orphan target), and Beru HTTP:

```bash
export SHADOW_NS=$(kubectl get shadowtest my-app-shadow -n default -o jsonpath='{.status.shadowNamespace}')

kubectl port-forward -n "$SHADOW_NS" svc/my-app-shadow-igris 8888:8888
kubectl port-forward -n "$SHADOW_NS" svc/my-app-shadow-control-a 8889:8888
kubectl port-forward -n beru-system svc/beru 8080:8080
```

| Local URL | Backend |
|-----------|---------|
| `http://127.0.0.1:8888` | Igris (`TARGET_URL`) — multicasts to all three roles |
| `http://127.0.0.1:8889` | control-a Envoy ingress only (`ORPHAN_TARGET_URL`) |
| `http://127.0.0.1:8080/healthz` | Beru HTTP health |

## Run the test

**Recommended** — wrapper starts port-forwards, preflights Beru `/healthz`, then runs k6:

```bash
cd tests/k6
./run-stress-test.sh
```

Quick smoke:

```bash
./run-stress-test.sh -e DURATION=30s
```

If port-forwards are already running in other terminals:

```bash
SKIP_PORT_FORWARD=1 ./run-stress-test.sh -e DURATION=30s
```

### Manual run (port-forwards required first)

Start the three `kubectl port-forward` commands in [Port-forwards](#port-forwards), then:

Quick smoke (30s):

```bash
k6 run -e DURATION=30s \
       -e TARGET_URL=http://127.0.0.1:8888 \
       -e ORPHAN_TARGET_URL=http://127.0.0.1:8889 \
       -e BERU_HEALTH_URL=http://127.0.0.1:8080/healthz \
       stress-test.js
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TARGET_URL` | `http://127.0.0.1:8888` | Igris base URL |
| `ORPHAN_TARGET_URL` | `http://127.0.0.1:8889` | Single-role (control-a) URL |
| `BERU_HEALTH_URL` | `http://127.0.0.1:8080/healthz` | Beru liveness probe |
| `TEST_PATH` | `/post` | HTTP path (JSON echo) |
| `DURATION` | `2m` | All scenario durations |
| `LARGE_PAYLOAD_MB` | `5` | Body size for `large_payload` (expect 413; Igris default max 2MB) |

### Scenarios (parallel)

| Scenario | VUs | Purpose |
|----------|-----|---------|
| `steady_state` | 10 | Stable JSON `{"user":"alice","price":10}` |
| `noise_generator` | 5 | Random `timestamp` / `uuid` per request |
| `large_payload` | 1 | 5MB body built **once in init context** (avoids k6 OOM) |
| `orphaned_traces` | 5 | Hits control-a only with `x-shadow-trace-id: k6-orphan-...` |
| `beru_health` | 1 req / 5s | `GET /healthz`; metric `beru_health_success` must stay **100%** |

## CI / exit codes

The script sets `thresholds: { beru_health_success: ['rate==1.0'] }`. Any failed Beru health probe causes k6 to exit **non-zero** — use that in pipelines:

```bash
k6 run --quiet -e DURATION=30s ... stress-test.js
echo "exit=$?"
```

For local debugging only, you may temporarily relax the threshold (not recommended for gates).

## Post-run checklist

1. **k6 exit code 0** and `beru_health_success` threshold passed.
2. **Beru logs** — no panics; steady traffic mostly `No regression`:

   ```bash
   kubectl logs -n beru-system deployment/beru --tail=200
   ```

3. **Orphan timeouts** — greppable trace IDs:

   ```bash
   kubectl logs -n beru-system deployment/beru --tail=500 | grep 'k6-orphan-'
   ```

   Expect lines like `Timed out waiting for Trace k6-orphan-...` with missing `control-b` / `candidate`.

4. **Memory (optional)** — Beru/Igris stay stable under load:

   ```bash
   kubectl top pod -n beru-system -l app.kubernetes.io/name=beru
   kubectl top pod -n "$SHADOW_NS" -l app.kubernetes.io/component=igris
   ```

## Noise filter note

With identical echo images (`examples/e2e-shadowtest.yaml`), control-a/b/candidate return the same JSON, so Beru often logs **“No noise fields”**. The `noise_generator` scenario still stresses ingest under high-cardinality **requests**. To exercise diff-of-diffs noise filtering live, use different `oldImage` / `newImage` so control-a and control-b responses diverge on `timestamp`.

## Related scripts

- [`scripts/stress-test.sh`](../../scripts/stress-test.sh) — grpcurl orphan flood, 10MB 413 check, `hey` burst (complementary).
- [`scripts/e2e-reset-kind.sh`](../../scripts/e2e-reset-kind.sh) — full Kind deploy.
- [`examples/e2e-pipeline-test.sh`](../../examples/e2e-pipeline-test.sh) — single-trace ingress validation.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|----------------|-----|
| `connection refused` on `:8888` / `:8889` / `:8080` | Port-forwards not running | Use `./run-stress-test.sh` or start the three `kubectl port-forward` commands |
| `beru_health_success` 0% | Beru not forwarded or old image without `/healthz` | `./run-stress-test.sh` preflights health; if **404**, run `kind load` + `kubectl rollout restart deployment/beru -n beru-system` |
| 200 on 5MB POST | Old Igris without 2MB limit | Rebuild Igris (`IGRIS_MAX_BODY_SIZE` default 2MB) |
| No `k6-orphan-` in logs | Wrong `ORPHAN_TARGET_URL` (hitting Igris/multicast) | Forward **control-a** service to 8889, not Igris |
| k6 OOM | Large body built per iteration | Confirm `largePayloadBody` is init-scoped in `stress-test.js` |
