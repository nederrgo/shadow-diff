# Shop — HTTP Egress Mock Store

Shop is a lightweight in-memory service that records production HTTP egress responses and replays them back to shadow workloads during Envoy egress interception. It lives in the shadow namespace, **always** deployed by Monarch (there is no `spec.recordAndReplay` field). After each mock reply it asynchronously reports the outbound call to Beru for HTTP egress diff-of-diffs.

## Role in the pipeline

```
Prod outbound HTTP
  → Kaisel eBPF egress capture (request + response paired)
  → Shop POST /v1/record_egress   ← seed path
                ↑
Shadow app
  → Envoy egress :10001 (BUFFERED body)
  → shop_ext_proc gRPC :50051     ← replay path
  → recorded response (or 599)
                │
                └── async POST /api/v1/egress/diff → Beru
```

Shop bridges prod observation and shadow replay. Beru handles ingress and AMQP egress diff-of-diffs; Shop owns HTTP egress **replay** and **reports** HTTP egress to Beru for analysis.

## Ports

| Port | Protocol | Endpoint | Purpose |
|------|----------|----------|---------|
| `:8080` | HTTP | `POST /v1/record_egress` | Kaisel seeds a captured prod response |
| `:8080` | HTTP | `GET /healthz` | Liveness check |
| `:50051` | gRPC | `ExternalProcessor_Process` | Envoy `shop_ext_proc` cluster — replay mock or return 599 |

## Mock key scheme

Each stored mock is keyed by:

```
trace:<traceID>:<METHOD>:<host>:<path>
```

- `traceID` — the W3C trace ID from the `traceparent` header, injected by Igris before multicasting. All three shadow roles for the same prod request share the same trace ID.
- `METHOD` — HTTP method (e.g. `POST`)
- `host` — request `Host` header without port
- `path` — URL path (e.g. `/v1/log`)

## Beru report payload

```json
{
  "trace_id": "...",
  "workload": "control-a|control-b|candidate",
  "protocol": "http",
  "shadow_test_name": "...",
  "payload": {
    "method": "POST",
    "host": "billing.internal",
    "path": "/v1/charges",
    "status": 201,
    "body": {"amount": 100}
  }
}
```

Signature in Beru: `http:{METHOD}:{path}`. Reporting is skipped when `BERU_HTTP_URL` is unset.

## Deployment

Monarch deploys Shop automatically for every ShadowTest and waits for `AvailableReplicas > 0`.

Envoy on each shadow pod gets a `shop_ext_proc` cluster pointing at `shop.<shadow-ns>.svc.cluster.local:50051` with `request_body_mode: BUFFERED`.

Image resolution:

1. `SHOP_IMAGE` env var on the Monarch controller (explicit override)
2. `shop:dev` when `MONARCH_MODE=dev`
3. `shop:latest` otherwise

## State

All mock state is **in-memory** only. State is lost if the Shop pod restarts.

## Building

```bash
cd pipeline/shop
make build          # go build ./cmd/shop
make docker-build   # builds shop:dev
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SHOP_GRPC_ADDR` | `:50051` | gRPC ext_proc listen address |
| `SHOP_HTTP_ADDR` | `:8080` | HTTP API listen address |
| `BERU_HTTP_URL` | (empty) | Beru HTTP base URL; when set, enables async egress reports |
| `SHADOW_TEST_NAME` | (empty) | Optional `shadow_test_name` on Beru reports |
