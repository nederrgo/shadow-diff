# Shop — HTTP Egress Mock Store

Shop is a lightweight in-memory service that records production HTTP egress responses and replays them back to shadow workloads during Envoy egress interception. It lives in the shadow namespace, deployed by Monarch alongside Recorder whenever `spec.recordAndReplay` is set on a `ShadowTest`.

## Role in the pipeline

```
Prod outbound HTTP
  → Pixie eBPF egress export
  → Recorder OTLP :4317
  → Shop POST /v1/record_egress   ← seed path
                ↑
Shadow app HTTP_PROXY
  → Envoy egress :10001
  → shop_ext_proc gRPC :50051     ← replay path
  → recorded response (or 599)
```

Shop bridges the gap between prod observation and shadow replay. Beru handles diff-of-diffs for ingress and MongoDB/AMQP egress; Shop handles HTTP egress exclusively.

## Ports

| Port | Protocol | Endpoint | Purpose |
|------|----------|----------|---------|
| `:8080` | HTTP | `POST /v1/record_egress` | Recorder seeds a captured prod response |
| `:8080` | HTTP | `GET /healthz` | Liveness check |
| `:50051` | gRPC | `ExternalProcessor_Process` | Envoy `shop_ext_proc` cluster — replay mock or return 599 |

## Mock key scheme

Each stored mock is keyed by:

```
trace:<traceID>:<METHOD>:<host>:<path>
```

- `traceID` — the W3C trace ID from the `x-shadow-trace-id` or `traceparent` header, injected by Igris before multicasting. All three shadow roles for the same prod request share the same trace ID.
- `METHOD` — HTTP method (e.g. `POST`)
- `host` — request `Host` header (e.g. `user-service.prod.internal:8080`)
- `path` — URL path (e.g. `/v1/log`)

This key means the same request from different roles will hit the same stored mock, which is the intended behaviour: all three shadow workers (control-a, control-b, candidate) see identical recorded responses when replaying the same prod egress call.

## Deployment

Monarch deploys Shop automatically when `spec.recordAndReplay` is non-empty on a `ShadowTest`. The deployment order is:

1. **Shop** — deployed first; Monarch waits for `AvailableReplicas > 0`
2. **Recorder** — deployed after Shop is ready; `SHOP_HTTP_URL` is set to `http://shop.<shadow-ns>.svc.cluster.local:8080`

Envoy on each shadow pod gets a `shop_ext_proc` cluster pointing at `shop.<shadow-ns>.svc.cluster.local:50051`. The egress listener uses this cluster for ext_proc calls (not the `beru_ext_proc` cluster, which handles ingress diff-of-diffs only).

Image resolution follows the standard Monarch helper-image pattern:

1. `SHOP_IMAGE` env var on the Monarch controller (explicit override)
2. `shop:dev` when `MONARCH_MODE=dev`
3. `shop:latest` otherwise

## State

All mock state is **in-memory** only. State is lost if the Shop pod restarts. For the E2E flow this is fine: Recorder re-seeds mocks from each new Pixie egress export cycle. A persistent store (e.g. Redis) could be substituted here if longer-lived replay is needed.

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
