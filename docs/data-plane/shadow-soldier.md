---
type: Architecture Specification
title: shadow-soldier Database Egress Capture
description: Plain-text TCP proxy sidecar that decodes MongoDB, PostgreSQL, Redis and MSSQL wire protocols inside each shadow pod and reports every query to Beru for diffing.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/shadow-soldier
tags: [data-plane, shadow-soldier, egress, database, mongodb, postgresql, redis, mssql, proxy]
timestamp: 2026-07-26T17:05:00Z
---

# shadow-soldier — Database Egress Capture

shadow-soldier is the L4b capture path. It runs as a sidecar in each shadow pod,
proxies the application's plain-text database connections to that role's
ephemeral dependency, decodes the wire protocol as bytes pass through, and posts
each query to Beru as an ordinary egress report.

The application container is untouched. The only thing Monarch changes is the
host in the injected connection string.

## Placement

```
shadow-<crNamespace>-<crName>, one pod per role
┌────────────────────────────────────────────────────────────────┐
│ initContainer iptables-setup   (unchanged — its first rule     │
│                                 RETURNs -d 127.0.0.1/8, so     │
│                                 proxy traffic is exempt)       │
├────────────────────────────────────────────────────────────────┤
│ app container (pristine)                                       │
│   MONGO_URL = mongodb://127.0.0.1:27017   ─┐                   │
│   REDIS_ADDR = 127.0.0.1:6379              │                   │
│   PG_DSN = postgres://…@127.0.0.1:5432     │                   │
├────────────────────────────────────────────┼───────────────────┤
│ shadow-soldier                             ▼                   │
│   listens 127.0.0.1:{27017,6379,5432,1433}                     │
│   dials mongodb-control-a.<shadowNS>.svc.cluster.local:27017   │
├────────────────────────────────────────────────────────────────┤
│ envoy-sidecar (unchanged)                                      │
└────────────────────────────────────────────────────────────────┘
         │ POST /api/v1/egress/diff
         ▼ beru-local.<shadowNS>.svc.cluster.local:8081
```

## Which dependencies are proxied

| `spec.dependencies[].type` | Protocol reported | Default port |
| --- | --- | --- |
| `mongodb`, `mongo` | `mongodb` | 27017 |
| `redis` | `redis` | 6379 |
| `postgres`, `postgresql` | `postgresql` | 5432 |
| `mssql`, `sqlserver` | `mssql` | (image and port required) |
| `rabbitmq` | — not proxied | 5672 |

RabbitMQ is deliberately excluded: its egress is already diffed by
`egress-relay-rabbitmq` off the broker Firehose, and it keeps its Service DNS
name. A ShadowTest with no proxied dependency gets no sidecar and keeps the
two-container pod it has today.

Two proxied dependencies cannot share a port — they would need the same loopback
listener — and `validateDependencies` rejects that at admission.

## Reporting contract

```json
POST http://beru-local.<shadowNS>.svc.cluster.local:8081/api/v1/egress/diff
{
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "workload": "candidate",
  "protocol": "postgresql",
  "signature": "postgresql:select:users",
  "shadow_test_name": "my-test",
  "payload": {
    "operation": "select", "target": "users",
    "raw_query": "SELECT * FROM users WHERE id = $1",
    "parameters": ["123"], "shadow_pod": "my-test-candidate-7d9f-x2k"
  }
}
```

| Field | Rule |
| --- | --- |
| `trace_id` | Bare 32-hex, lowercase — **not** the full traceparent. Beru stores it verbatim as the SQLite grouping key, so a full traceparent would bucket separately from Envoy's ext_proc reports |
| `workload` | The shadow role. Beru correlates on role, never on pod name |
| `signature` | Supplied by the sidecar, which decoded the protocol and knows it exactly |
| `payload` | For MongoDB, the command document verbatim; for everything else, the structured object above |

MongoDB payloads pass through unwrapped so Beru's `mongoPayloadsEqual` can strip
the per-connection fields (`_id`, `lsid`, `comment`, `$db`) before comparing.

### Why the sidecar supplies its own signature

Beru can derive a database signature from a payload, but for a non-Mongo protocol
that means taking the first non-`$` string key in sorted order. Raw SQL in the
payload sorts ahead of the operation and changes whenever a literal in the query
changes, so the derived signature would drift between roles and produce
`MISMATCH_SIGNATURE` verdicts describing nothing. `signature` is optional on the
endpoint; producers that do not decode the protocol still omit it.

### Why port 8081

The shadow pod's `iptables-setup` init container installs
`-t nat -A OUTPUT -p tcp --dport 8080 -j REDIRECT --to-port 10001`. The rule is
per network namespace, not per process, so a sidecar posting to `beru-local:8080`
would be redirected into Envoy's egress listener and answered
`502 egress: no mock found` — silent, total telemetry loss. `beru-local` therefore
publishes a second Service port, `8081 → targetPort 8080`, that the redirect does
not match.

## Trace correlation

Trace context is recovered by scanning the decoded command bytes for a W3C
traceparent:

```
00-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}
```

One regex covers every carrier the platform relies on — a SQL comment
(`/* traceparent='00-…' */`), a BSON `$comment`, a TDS batch comment — which is
the same mechanism Beru already used for MongoDB spans. A query with no
recoverable traceparent cannot be correlated with the other two roles, so it is
dropped and counted, matching Kaisel's policy for untraced HTTP captures.

## Protocol decoding

| Protocol | Decoded | Library |
| --- | --- | --- |
| PostgreSQL | `Query` (simple) and `Parse`+`Bind` (extended, paired so parameters are reported) | `github.com/jackc/pgx/v5/pgproto3` |
| MongoDB | OP_MSG kind-0 body section → BSON command document | `go.mongodb.org/mongo-driver/bson` |
| Redis | RESP arrays and inline commands | none — hand-decoded |
| MSSQL | TDS SQLBatch (`0x01`); RPCRequest (`0x03`) for `sp_executesql` | none — hand-decoded |

Redis and MSSQL are hand-decoded on purpose. RESP array parsing is a few dozen
lines of `bufio`, and `microsoft/go-mssqldb` keeps all TDS framing unexported in
its root package, so there is no protocol API to import.

### PostgreSQL opportunistic SSL

A PostgreSQL driver's first act is an 8-byte `SSLRequest` (request code
`80877103`). shadow-soldier answers it itself with a single `'N'` and does **not**
forward the probe, so the upstream sees an ordinary plain-text connection
beginning at the `StartupMessage`. `GSSENCRequest` (`80877104`) is answered the
same way.

Answering locally rather than relaying is what keeps the proxy transparent: a
forwarded probe would need the upstream's own reply relayed back before piping
could start, leaving the two sockets a handshake out of step. A driver using
`sslmode=require` will refuse the downgrade; shadow dependencies are plaintext by
construction, so `prefer` (the common default) is what this path is for.

## Do no harm

The application's database socket is never delayed, blocked, or broken by
observation.

| Mechanism | Effect |
| --- | --- |
| Bounded non-blocking tap | The parser reads a side-channel of the copy loop. On overflow the tap closes rather than dropping a chunk — a gap mid-stream leaves framing unrecoverable, so every later report would be garbage |
| Parser isolation | A parser error or panic is recovered; the connection is demoted to plain byte forwarding for the rest of its life |
| Non-blocking enqueue | A full report queue drops and counts; it never applies backpressure to the socket |
| Connection cap | Accepts are refused past `SOLDIER_MAX_CONNS` rather than queued |
| Pooled buffers | 32 KB copy buffers come from a `sync.Pool`; 64 KB per-frame parse cap |
| `debug.SetMemoryLimit` | Soft limit (24 MiB default) makes GC work harder near the pod limit instead of letting the kernel OOM-kill the pod and take the app container with it |

Every drop is counted and logged at shutdown. A run that reported nothing must be
distinguishable from a run that had nothing to report.

### Per-trace ordering

Reports are sharded to workers by `fnv32a(trace_id) % workers`, so all reports
for one trace are posted by one goroutine in order. Beru pairs egress reports **by
index within a signature bucket**, ordered by the arrival time it stamps itself,
so a shared queue drained by N workers would compare query 1 against query 2 and
report a payload mismatch that is purely an artefact of concurrency. This mirrors
the FNV sharding Beru's own `TraceRouter` uses.

### Retries

Beru's `AppendReport` is a plain INSERT with no idempotency key, so a report
delivered twice reads as an extra egress call — a regression that never happened.
Retries are limited to failures where the request provably did not reach the
handler: connection refused, DNS failure, reset, and 5xx. A **timeout is not
retried** — it cannot be told apart from "Beru accepted it and was slow to
answer", and a false extra egress is worse than a missing one.

## Configuration

| Env | Meaning |
| --- | --- |
| `SOLDIER_ROUTES` | JSON `[{"protocol":…,"listen":…,"upstream":…}]`, rendered per role by Monarch |
| `SHADOW_ROLE` | `control-a` \| `control-b` \| `candidate` → the report's `workload` |
| `SHADOW_TEST_NAME` | → the report's `shadow_test_name` |
| `BERU_HTTP_URL` | `http://beru-local.<shadowNS>.svc.cluster.local:8081` |
| `POD_NAME` | Downward API (`metadata.name`); debugging only |
| `SOLDIER_MEMORY_LIMIT` | Soft heap limit in bytes (default 24 MiB) |
| `SOLDIER_MAX_CONNS`, `SOLDIER_QUEUE_SIZE`, `SOLDIER_WORKERS`, `SOLDIER_TAP_CHUNKS` | Tunables |
| `SOLDIER_DIAL_TIMEOUT`, `SOLDIER_IDLE_TIMEOUT`, `SOLDIER_HTTP_TIMEOUT` | Durations |

Image resolution follows the standard chain: `spec.shadowSoldier.image` →
`SHADOW_SOLDIER_IMAGE` on the controller → `shadow-soldier:dev|:latest` per
`MONARCH_MODE`.

## Limitations

| Limitation | Detail | Mitigation |
| --- | --- | --- |
| **Redis is effectively untraced** | RESP has no comment or metadata field, so there is nowhere for a traceparent to ride. The parser works; trace attribution does not | Reports drop as untraced unless the app embeds a traceparent in a key or argument. Use MongoDB or PostgreSQL to exercise database diffing |
| **Plain text only** | Packet-level decoding cannot read TLS | Monarch provisions dependencies plaintext, and the Postgres `'N'` reply enforces it for drivers that ask |
| **Env-var rewriting only** | An app with a hardcoded connection string bypasses the proxy entirely | Upgrade path is an iptables REDIRECT on database ports plus `SO_ORIGINAL_DST` |
| **Naive SQL classification** | Table extraction is a keyword scan: a CTE reports the first `FROM`, a join reports the first table | Stable across roles, which is the property signatures need. Upgrade path is a real SQL parser |
| **MSSQL RPC is partial** | Only `sp_executesql` yields statement text; a call to a user stored procedure reports the procedure name alone | Upgrade path is a full TDS parameter walk |
| **Best-effort under pressure** | A tap overflow costs one connection's reports; a full queue drops | Both counted and logged, never silent |

## Verification

| Suite | Command | Covers |
| --- | --- | --- |
| Unit | `make -C pipeline/shadow-soldier test` | SSLRequest handshake, each parser, tap non-blocking + copy semantics, reporter sharding/retry, end-to-end proxying against a real listener |
| Monarch | `make -C pipeline/monarch test` | Route rendering per role, loopback env rewrite, sidecar presence/absence, downward-API pod name |
| Beru | `make -C pipeline/beru test` | Supplied-signature passthrough and derivation fallback |

The highest-value unit tests are the PostgreSQL handshake cases: if the `'N'`
reply is missing or the probe is forwarded, every pgx client fails to connect
while every other test still passes.

# Citations

- [`pgproto3`](https://pkg.go.dev/github.com/jackc/pgx/v5/pgproto3) — PostgreSQL frontend message decoding.
- [SSLRequest message format](https://www.postgresql.org/docs/current/protocol-message-formats.html) — the 8-byte probe and the single-byte `'S'`/`'N'` reply.
- [MongoDB OP_MSG](https://www.mongodb.com/docs/manual/reference/mongodb-wire-protocol/) — opcode 2013, kind-0 body and kind-1 document sequences.
- [RESP protocol](https://redis.io/docs/latest/develop/reference/protocol-spec/) — array and inline command forms.
- [`debug.SetMemoryLimit`](https://pkg.go.dev/runtime/debug#SetMemoryLimit) — the soft limit backing the OOM defence.
- [Beru egress diff ingest](https://github.com/shadow-diff/monarch/blob/main/pipeline/beru/internal/api/http.go) — `/api/v1/egress/diff` and the optional `signature` field.
- [Kaisel eBPF capture](/data-plane/kaisel-ebpf.md) — the HTTP capture path, and the untraced-drop policy this component follows.
