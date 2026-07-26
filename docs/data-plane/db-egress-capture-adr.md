---
type: Architectural Decision Record
title: Database Egress Capture via In-Pod Proxy
description: Why Shadow-Diff captures database egress with a plain-text TCP proxy sidecar instead of an OTLP span pipeline or an Envoy protocol filter, and what was retired alongside it.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/shadow-soldier
tags: [adr, data-plane, shadow-soldier, egress, database, mongodb, otlp, beru]
timestamp: 2026-07-26T17:10:00Z
---

# Database Egress Capture via In-Pod Proxy

## Context

Shadow-Diff diffed HTTP egress (Kaisel → Shop) and AMQP egress
(egress-relay-rabbitmq → Beru). Database egress had no verdict at all.

[/data-plane/pixie-removal.md](/data-plane/pixie-removal.md) withdrew MongoDB
egress diffing when Pixie was deleted, and kept Beru's analysis half — the OTLP
receiver on `:4317`, the MongoDB wire parser, the sequence diff — explicitly as
the re-entry point for a future capture path:

> Restoring it requires a new capture path only. […] Any future capture path can
> point at the same unchanged port.

Two candidates existed for that path, and neither fit:

* **Kaisel** is an out-of-band packet sniffer that parses HTTP only. It cannot
  read TLS, and extending it to four database wire protocols would put stateful
  per-connection protocol decoding into a node-level DaemonSet observing every
  pod on the node.
* **The roadmap's Envoy `mongo_proxy` network filter + gRPC ALS** covers MongoDB
  alone, and pins the set of decodable protocols to whatever Envoy's filter chain
  happens to ship.

## Decision

Capture database egress with **shadow-soldier**, a plain-text TCP proxy sidecar
inside each shadow pod, and **delete the OTLP MongoDB route** it replaces.

Monarch points the application's injected connection string at `127.0.0.1`; the
sidecar listens there, forwards to the real per-role dependency Service, decodes
the wire protocol in passing, and posts each query to Beru's existing
`/api/v1/egress/diff`. One optional field was added to that endpoint —
`signature` — so a producer that decoded the protocol is authoritative for its own
`protocol:operation:target`.

This sits inside the manifesto's boundary rather than against it: *"We strictly
isolate proxy overhead to the shadow namespace where we can afford fine-grained
proxy control."* Production capture stays out-of-band eBPF. An inline proxy is
only acceptable here because the blast radius is a shadow pod.

| Removed | Kept |
| --- | --- |
| `pipeline/beru/internal/otlp/` (receiver, MongoDB wire parser, HTTP handler) | Beru's MongoDB diff (`internal/v2/diff/mongo_compare.go`) |
| Beru's `:4317` OTLP gRPC listener and `POST /v1/traces` | `MongoSignature` / `MongoHints`, reached from `EgressSignature` and the wire-ingest path |
| `FromMongoEgress`, `MongoOperationFromStatement` | `/api/v1/egress/diff`, now with an optional `signature` |
| beru-local's `otlp-grpc` port; `beruOTLPEndpointFor` / `beruOTLPHTTPEndpointFor` (already dead) | beru-local's gRPC and HTTP ports |
| `go.opentelemetry.io/proto/otlp` from Beru's `go.mod` | The `mongo_spans.json` signature fixture, moved to `internal/v2/report/testdata/` |

The OTLP receiver was safe to delete whole because `Export()` skipped every span
where `isMongoSpan()` was false — it had no non-MongoDB purpose. Its Monarch-side
endpoint helpers were referenced only by their own tests; nothing ever set
`OTEL_EXPORTER_OTLP_ENDPOINT` on a shadow pod.

## Consequences

### Database egress diffing works, and is no longer MongoDB-only

MongoDB, PostgreSQL, Redis and MSSQL queries are compared across the three roles
through the same diff-of-diffs engine as HTTP and AMQP: extra queries, missing
queries, N+1 loops and changed statements all produce verdicts. Beru's diff
engine needed no protocol work — it is keyed on `protocol` + `signature` +
payload.

### Redis has a parser but no trace attribution

RESP has no comment or metadata field, so there is nowhere for a W3C traceparent
to ride. Redis reports are dropped as untraced unless the application embeds a
traceparent in a key or argument value. This is a protocol limit, not an
implementation gap, and it is why MongoDB is the E2E subject.

### The application must be pointed at loopback

The rewrite is confined to `dependencyEnvVarsForRole`. An application with a
hardcoded connection string bypasses the proxy silently. Closing that needs an
iptables REDIRECT on database ports plus `SO_ORIGINAL_DST`.

### Beru's ingest port for in-pod sidecars is 8081

The shadow pod's iptables rules REDIRECT outbound `:8080` into Envoy's egress
listener, per network namespace rather than per process. Reporting on `:8080`
from inside a shadow pod is answered `502 egress: no mock found`. beru-local now
publishes `8081 → targetPort 8080` for that traffic. Shop and
egress-relay-rabbitmq are unaffected — they run in their own pods and keep using
`:8080`.

### Beru accepts no OpenTelemetry at all

Beru has one fewer port, one fewer protocol and one fewer dependency. Any future
OTel-based producer would have to reintroduce a receiver rather than reuse a
dormant one — a deliberate trade for deleting ~850 lines that nothing fed.

### PostgreSQL dependencies are now first-class

`spec.dependencies[].type: postgres` resolves to `postgres:16-alpine` on 5432 and
is given `POSTGRES_HOST_AUTH_METHOD=trust`, without which the image refuses to
initialise and the readiness gate never opens. Trust auth is confined to an
ephemeral database inside the shadow namespace.

# Citations

- Component spec: [/data-plane/shadow-soldier.md](/data-plane/shadow-soldier.md)
- Prior withdrawal of MongoDB egress diffing: [/data-plane/pixie-removal.md](/data-plane/pixie-removal.md)
- HTTP capture path and the untraced-drop policy: [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md)
- Manifesto constraint on proxy placement: [/manifesto.md](/manifesto.md)
- [`mongoPayloadsEqual`](https://github.com/shadow-diff/monarch/blob/main/pipeline/beru/internal/v2/diff/mongo_compare.go) — the analysis half kept from the Pixie era.
