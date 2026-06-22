# Siphon

**Siphon** is the **L1 — capture** ingress path for HTTP ShadowTests. **Pixie** eBPF (Vizier PEM) captures production `http_events`; **pixie-stream-bridge** runs `px.export` OTLP traces to Siphon **:4317**; Siphon parses span attributes and **HTTP POST**s to **igris-http (L2)** for multicast to shadow clones.

RabbitMQ ShadowTests do not use Siphon — AMQP uses broker-native routing ([igris-rabbitmq](../igrises/igris-rabbitmq/)).

See [docs/architecture/ARCHITECTURE.md](../../docs/architecture/ARCHITECTURE.md).

---

## Role in the pipeline

```
  prod my-prod-app:80
        │
        ▼ (Pixie PEM eBPF — http_events)
  pixie-stream-bridge (host: px run + px.export OTLP traces)
        │
        ▼ gRPC OTLP (gzip)
  Siphon :4317  ──HTTP POST──►  igris-http  ──►  control-a / control-b / candidate
```

| Component | Purpose |
| --------- | ------- |
| `PixieStreamRule` CR | Monarch declares prod pod labels, ports, and `otelEndpoint` (shadow Siphon Service) |
| **pixie-stream-bridge** | Reconciles rules → PxL scripts; polls `px run` (not in-cluster) |
| Siphon OTLP receiver | One gRPC server for logs + traces → `HTTPRecord` → igris forwarder |
| Shadow `Service/siphon` | Cluster DNS export target for Pixie (`siphon.<shadow-ns>.svc.cluster.local:4317`) |

Monarch provisions the **Service** and **PixieStreamRule**; you deploy the **Siphon Deployment** in the shadow namespace (E2E manifest: `testing/scripts/manifests/siphon-otlp-e2e/siphon-deployment.yaml`). The legacy `pipeline/siphon/deploy/daemonset.yaml` hostNetwork path is superseded.

---

## Layout

```
siphon/
  cmd/siphon/              OTLP gRPC :4317 bootstrap (registers gzip decompressor for Pixie)
  internal/
    receiver/              OTLP logs + traces → HTTPRecord
    forwarder/             POST to SIPHON_IGRIS_BASE_URL
```

---

## Build and test

```sh
make -C pipeline/siphon build test
make siphon-docker-build SIPHON_IMG=siphon:dev
```

---

## Configuration

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `SIPHON_IGRIS_BASE_URL` | (required) | igris-http base URL for forwarded requests |
| `SIPHON_OTLP_GRPC_ADDR` | `:4317` | OTLP gRPC listen address |
| `SIPHON_WORKER_COUNT` | `8` | Forward worker pool size |
| `SIPHON_JOB_QUEUE_SIZE` | `1024` | Queue before drop |

OTLP **trace** span attributes (from Pixie PxL): `http.request.method`, `url.path`, `x-shadow-trace-id`, `http.request.body` (string). Siphon forwards `x-shadow-trace-id` to Igris as an HTTP header.

**Pixie PxL notes:** use `px.pluck(df.req_headers, 'x-shadow-trace-id')`; filter prod pods with `px.contains(df.pod, '<app>')` (not `df.service`); precompute `df.end_time = df.time_ + df.latency` for `px.otel.trace.Span`. Template: `testing/scripts/manifests/pixie-bridge/configmap.yaml`.

---

## Verification

**Pixie + Minikube (verified eBPF path):**

```sh
MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh   # Vizier in pl + px auth
./testing/scripts/e2e-reset-minikube.sh --no-reset

# background bridge (requires px auth — use px auth login --manual on WSL)
nohup ./testing/scripts/pixie-stream-bridge.sh > .cache/pixie-bridge/bridge.log 2>&1 &

./testing/scripts/e2e-siphon-otlp-ingress-test.sh
```

Requires **kvm2** (or virtualbox) Minikube driver, **flannel** CNI, Pixie Vizier healthy, and `siphon:dev` built into the minikube docker daemon (`eval $(minikube docker-env)`).

**Local smoke (no cluster, no Pixie):**

```sh
./testing/scripts/e2e-siphon-otlp-local-smoke.sh
```

See [docs/verification/VERIFICATION.md](../../docs/verification/VERIFICATION.md) for prod Service curl and Igris log checks.

---

## Related reading

- [pipeline/monarch/DEPLOYMENT.md](../monarch/DEPLOYMENT.md) — `PixieStreamRule`, `spec.siphon`, `status.siphonPhase`
- [pipeline/igrises/README.md](../igrises/README.md) — L2 ingress hub
