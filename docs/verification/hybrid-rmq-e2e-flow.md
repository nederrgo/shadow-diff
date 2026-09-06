---
type: Architecture Specification
title: Hybrid RMQ E2E Test Flow
description: End-to-end record→replay flow for Node.js and Python hybrid bats suites — MinIO capture, CR mode switch, Postgres-backed RMQ/Mongo count regressions, plus RMQ+Shop sampling.
resource: https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress
tags: [verification, e2e, bats, hybrid, rabbitmq, kaisel, shop, shadow-soldier, mongodb, sampling, postgres, record-replay]
timestamp: 2026-08-03T08:50:00Z
---

# Hybrid RMQ E2E Test Flow

The hybrid suites exercise a **RabbitMQ-ingress** ShadowTest with Mongo + HTTP egress record/replay. Node and Python share the same shape; only prod manifests and worker images differ. A separate sampling suite proves the same path under `samplePercentage: 10`.

| Suite | Bats file | ShadowTest | Prod worker | Downstream HTTP |
|-------|-----------|------------|-------------|-----------------|
| Node | [`nodejs_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/nodejs_hybrid.bats) | `bats-nodejs-hybrid` | `nodejs-prod-worker` | `user-service-nodejs` / Host `user-service-nodejs.default.internal` |
| Python | [`python_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/python_hybrid.bats) | `bats-python-hybrid` | `python-prod-worker` | `user-service-python` / Host `user-service-python.default.internal` |
| Sampling hybrid | [`rmq_sampling_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/rmq_sampling_hybrid.bats) | `bats-rmq-sampling-hybrid` | `nodejs-prod-worker` | `user-service-nodejs` / Host `user-service-nodejs.default.internal` |

Run one suite:

```bash
SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-one FILE=e2e/rabbitmq-ingress/nodejs_hybrid.bats
SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-one FILE=e2e/rabbitmq-ingress/python_hybrid.bats
SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-one FILE=e2e/rabbitmq-ingress/rmq_sampling_hybrid.bats
```
---

## What “hybrid” means

One ShadowTest wires **four concerns** in a single prod worker path:

1. **Ingress** — record: prod RMQ → igris-rabbitmq → S3; replay: S3 → three shadow brokers  
2. **HTTP egress record/replay** — prod worker HTTP → Kaisel → Shop (S3 in record; mocks in replay via Envoy `shop_ext_proc`)  
3. **RabbitMQ egress** — shadow Firehose → egress-relay → Beru (replay stack); candidate intentionally publishes twice  
4. **MongoDB egress** — shadow-soldier proxies each role's Mongo connection and reports the decoded command to beru-local; candidate intentionally inserts a second document per order

There is **no** `igris-http` on these fixtures (RMQ-only input); the "HTTP ingress" test name is inherited from the shared helper naming, not an actual igris-http path.

---

## Setup flow (`setup_file`)

```mermaid
flowchart TD
  plat[ensure_platform_ready + minio] --> prod[Deploy prod RMQ Mongo user-service worker]
  prod --> onlyOne[Delete competing language worker]
  onlyOne --> st[Apply ShadowTest mode=record + storage]
  st --> wait[wait_shadowtest_ready Kaisel]
  wait --> beru[wait_local_beru_rollout + igris-rabbitmq + Shop]
```

Prod stack (default namespace):

- `rmq-prod-broker`, `mongo-prod`
- Downstream stub (`user-service-*`)
- Language worker that consumes `order.created` and performs Mongo + HTTP + RMQ egress

`setup_file` brings up the **record** stack: unbound→bound prod shadow queue, igris-rabbitmq, Shop, beru-local, `KaiselRule`. Each `@test` that needs A/B/C calls `kaisel_ensure_record_mode` → publish → `kaisel_switch_to_replay`, which provisions control-a/b/candidate, per-role deps, and egress-relay.

Competing workers share the same prod `orders` queue — each suite deletes the other language’s prod Deployment so only one consumer is active.

---

## Per-order runtime flow

Every `@test` that drives traffic starts with `publish_rmq_order(trace_id, order_id)` (W3C `traceparent` on the AMQP message).

```mermaid
sequenceDiagram
  participant Test as Bats
  participant ProdRMQ as Prod RMQ
  participant ProdW as Prod worker
  participant User as user-service
  participant Kaisel as Kaisel eBPF
  participant Shop as Shop
  participant Igris as igris-rabbitmq
  participant Shadow as Shadow workers
  participant Beru as beru-local

  Test->>ProdRMQ: publish order.created + traceparent
  ProdRMQ->>ProdW: consume
  ProdW->>ProdW: mongo insert (+ candidate N+1 in shadow only)
  ProdW->>User: HTTP POST Host=logical replay host
  Kaisel-->>Shop: POST /v1/record_egress (request+response pair)
  Rec->>Shop: POST /v1/record_egress
  Note over Test,Shop: wait_kaisel_egress_seed sees egress recorded

  ProdRMQ->>Igris: shadow queue bind / fan-out
  Igris->>Shadow: same message + traceparent
  Shadow->>Shop: HTTP via Envoy shop_ext_proc
  Shop-->>Shadow: recorded 200 mock
  Shadow->>Beru: shadow-soldier Mongo egress + RMQ Firehose relay
```

HTTP capture uses the dual-branch egress PxL (client `trace_role==1` + server `trace_role==2` scoped by remote client pod). See [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md).

---

## What each `@test` asserts

One ShadowTest CR per file. Record-phase tests stay in `mode: record`; replay-phase tests call `_hybrid_record_then_replay` (ensure record → publish → Kaisel egress seed → `kaisel_switch_to_replay both`).

### Record: RMQ ingress + HTTP egress flush to MinIO

`publish_rmq_order` + `wait_kaisel_egress_seed`, then `e2e_assert_session_objects both` under `sessions/<currentSessionID>/{ingress,egress}/`.

### Replay: HTTP egress reaches all shadow roles

RMQ ingress + HTTP Shop replay (not igris-http). For control-a/b/candidate: `assert_worker_http_replay` (`order_id=…`, `http egress via=replay status=200`).

### Replay: RabbitMQ egress count regression in Postgres

Same path; `beru_wait_verdict_settled … rabbitmq --expect-status=MISMATCH --expect-count-regression=1`. Candidate double-publishes on `egress-events` / `order.shipped`.

### Replay: MongoDB egress count regression in Postgres

Same path; `beru_wait_verdict_settled … mongodb --expect-status=MISMATCH --expect-count-regression=1`. Candidate inserts a second `candidate_n1_loop` document per order.

---

## Sampling hybrid (`rmq_sampling_hybrid.bats`)

Same Node prod stack and HTTP record/replay path, with `spec.samplePercentage: 10`. Two gates share `github.com/shadow-diff/sample`: decode the 32-hex W3C trace id to 16 bytes, `V = FNV-1a-64(bytes) & 0xFF`, keep iff `(V*100)<(N*256)`:

1. **igris-rabbitmq** — drops out-of-sample AMQP before S3 capture (record); replay only fans out recorded messages  
2. **Kaisel egress** — drops out-of-sample Shop seeds (`POST /v1/record_egress`)

Prod still consumes and egresses every published order; absence asserts cover shadow pods and Shop only.

| Golden trace ID | V | At 10% | Expect |
|-----------------|---|--------|--------|
| `00000000000000000000000000000087` | `0` | keep | MinIO ingress+egress; Shop seed; roles `assert_worker_http_replay`; HTTP egress `MATCH` in Postgres |
| `000000000000000000000000000000f9` | `26` | drop | No shadow `order_id` / trace hex; no Kaisel `egress recorded` for `trace:<id>:` |

Intentional RMQ/Mongo count regressions are covered by the non-sampling hybrid suites via `beru_wait_verdict_settled`.

---

## Assertion cheat sheet

| Signal | Where |
|--------|--------|
| Session objects | MinIO `e2e_assert_session_objects` / `minio_wait_objects` |
| Shop seeded | Kaisel: `egress recorded ... hash=trace:<id>:…` |
| Shadow HTTP replay | Shadow app: `http egress via=replay status=200` |
| Mongo / RMQ / HTTP verdicts | `beru_wait_verdict_settled` → beru-local `GET /api/v1/traces/…` (Postgres) |

---

## Citations

- Node suite: [`testing/bats/e2e/rabbitmq-ingress/nodejs_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/nodejs_hybrid.bats)
- Python suite: [`testing/bats/e2e/rabbitmq-ingress/python_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/python_hybrid.bats)
- Sampling hybrid suite: [`testing/bats/e2e/rabbitmq-ingress/rmq_sampling_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/rmq_sampling_hybrid.bats)
- Traffic helpers: [`testing/bats/lib/traffic.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/traffic.bash) — `publish_rmq_order`; and [`testing/bats/lib/kaisel.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/kaisel.bash) — `wait_kaisel_egress_seed` / `kaisel_assert_egress_recorded`
- Node worker N+1 behavior: [`testing/example-apps/nodejs-hybrid-worker/index.js`](https://github.com/shadow-diff/monarch/tree/main/testing/example-apps/nodejs-hybrid-worker/index.js)
- Egress record/replay: [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md)
- MongoDB egress capture: [/data-plane/shadow-soldier.md](/data-plane/shadow-soldier.md)
- Bats harness: [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md)
