---
type: Architecture Specification
title: Tusk — Topology and Diff BFF
description: The Go gateway that fans Monarch topology over WebSockets and serves ShadowDiff REST/WS from shared Postgres LISTEN/NOTIFY.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/tusk
tags: [architecture, control-plane, tusk, websocket, grpc, topology, ui, postgres, diffs]
timestamp: 2026-08-02T06:20:00Z
---

# Tusk — Topology and Diff BFF

Tusk sits between Monarch's status stream, shared Beru Postgres, and [The System](/control-plane/the-system.md) dashboard. It converts `monarch.v1.MonarchStatusService` updates into React Flow graphs, and reads Beru's UI projection tables (`shadow_sessions`, `traces`, `diff_reports`) for the ShadowDiff page.

Tusk is a **cluster-wide singleton** in `monarch-system`, deployed from `pipeline/tusk/deploy/`. Like Kaisel, and unlike the per-ShadowTest workloads Monarch provisions, it is not scoped to any one test.

## Ports and endpoints

| | |
|---|---|
| HTTP / WebSocket | `:8082` (`TUSK_HTTP_ADDR`) |
| Monarch upstream | `MONARCH_GRPC_ADDR`, default `monarch-status-grpc.monarch-system.svc.cluster.local:9090` |
| Postgres | `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSLMODE` (same keys as beru-local) |
| `GET /ws/monitor?test=&namespace=` | streams topology graphs; omit both params to watch every ShadowTest |
| `GET /api/v1/sessions` | lists `shadow_sessions` (newest first) |
| `GET /api/v1/diffs?session_id=` | joined `diff_reports` + `traces` for one session |
| `GET /ws/diffs?session_id=` | summary snapshot, then live `{type:summary\|verdict}` frames |
| `GET /healthz` | 200 `ok` |

CORS and WebSocket `CheckOrigin` allow any `http(s)://localhost[:port]` or `http(s)://127.0.0.1[:port]` Origin so The System works from Vite (`:3000`) and Nginx (`:80`). Plain HTTP routes reflect an allowed request Origin, or default to `http://localhost:3000` when Origin is absent. A request with no `Origin` (probes, `websocat`, tests) is allowed for upgrades.

## Control-plane-only mode

When `DB_HOST` / `DB_USER` / `DB_NAME` are unset (or Postgres open fails), Tusk logs a warning and keeps serving topology. Diff REST returns `503`; `/ws/diffs` is unavailable. The Deployment mounts Secret `beru-postgres` via `envFrom` with `optional: true`.

## Stream topology

One gRPC stream serves the whole process regardless of how many browsers connect. It watches **all** ShadowTests and Tusk does per-client filtering locally; a stream per browser would multiply load on the operator for nothing. The stream reconnects with capped exponential backoff (1s → 30s), so a Monarch rollout degrades the feed rather than ending it.

Tusk caches the latest graph per ShadowTest. A browser connecting mid-flight receives the cached graph immediately, then live updates — mirroring the snapshot Monarch sends Tusk's own stream. Without the cache, a client attaching to a converged ShadowTest would see an empty canvas.

Delete lifecycle on the wire:

| Phase | Meaning | Hub behaviour |
|-------|---------|---------------|
| `Deleting` | Teardown in progress (CR still present) | Cache upsert; UI shows a Deleting badge |
| `Deleted` | Finalizers removed; CR gone | Evict cache entry; still fan out the tombstone so open sockets clear |

## ShadowDiff data path

Beru projects verdicts into Postgres and emits `NOTIFY verdict_events` with `{"session_id","trace_id","verdict"}`. Tusk:

1. Queries projection tables for REST hydrate (`GET /api/v1/sessions`, `GET /api/v1/diffs`).
2. Holds a dedicated `pgx` connection on `LISTEN verdict_events` (reconnect 1s → 30s).
3. Refreshes `SessionSummary` and fans frames through `DiffHub` (keyed by `session_id`, same non-blocking buffer policy as the topology hub).

WebSocket frames:

```json
{ "type": "summary", "session_id": "…", "total": 10, "match": 7, "mismatch": 2, "voided": 1 }
{ "type": "verdict", "session_id": "…", "trace_id": "…", "verdict": "MISMATCH" }
```

Payloads stay on the REST path; the UI refetches `/api/v1/diffs` when a new `verdict` frame arrives.

## Graph model

```
TopologyGraph { testName, namespace, phase, bootStep, mode, message, nodes[], edges[] }
Node          { id, type, label, status }
Edge          { id, source, target, animated }
```

Node `status` is one of `Ready`, `Provisioning`, `Failed`, `Disabled`, `Degraded`, resolved in this order: not participating in the current mode → `Disabled`; component flag true → `Ready`; test phase Failed → `Failed`; otherwise `Provisioning`. The Kaisel node reads `kaisel_phase` rather than the boolean so it can render `Degraded`.

Mode selects both the node set and the edge set:

| Mode | Edges | Roles |
|------|-------|-------|
| `record` | `target-app → kaisel`, `kaisel → igris`, `kaisel → shop`, `igris → beru` | rendered `Disabled` |
| `replay` | `igris → {control-a, control-b, candidate}`, each role `→ beru` and `→ shop` | driven by `shadow_roles_ready` |

Shadow roles are always emitted as nodes, greyed out in record mode, so the canvas keeps a stable shape across a mode switch instead of nodes appearing and vanishing.

`animated` is true only when both endpoints are `Ready`, so the graph animates exactly the paths where traffic can flow.

Both the node list and edge table are package-level data in `pkg/topology/generator.go`; changing the rendered shape is one edit.

## Backpressure

Both fan-out layers use the same policy: a non-blocking send into a bounded per-consumer buffer (16 in Monarch's hub, 8 in Tusk's). A consumer that stops reading loses frames rather than stalling the producer. Graphs and summaries are whole-state, so a dropped frame is superseded by the next; the trade-off is that a permanently slow client shows a stale view rather than a lagging one.

# Citations

- [pipeline/tusk/pkg/topology/generator.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/tusk/pkg/topology/generator.go) — node/edge model
- [pipeline/tusk/pkg/server/monarch_client.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/tusk/pkg/server/monarch_client.go) — single stream, backoff reconnect
- [pipeline/tusk/pkg/server/websocket.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/tusk/pkg/server/websocket.go) — routes, CORS, origin check
- [pipeline/tusk/pkg/db/](https://github.com/shadow-diff/monarch/tree/main/pipeline/tusk/pkg/db) — Postgres queries + LISTEN
- [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md) — projection tables + `verdict_events`
- [/control-plane/monarch-status-stream.md](/control-plane/monarch-status-stream.md) — the upstream gRPC contract
- [/control-plane/the-system.md](/control-plane/the-system.md) — browser UI consuming `/ws/monitor` and `/diffs`
