---
type: Architecture Specification
title: Hybrid RMQ E2E Test Flow
description: End-to-end data and assertion flow for Node.js and Python hybrid bats suites — RMQ ingress, Kaisel HTTP record/replay, RabbitMQ egress regressions.
resource: https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress
tags: [verification, e2e, bats, hybrid, rabbitmq, kaisel, shop]
timestamp: 2026-07-12T16:20:00Z
---

# Hybrid RMQ E2E Test Flow

The hybrid suites exercise a **RabbitMQ-ingress** ShadowTest with Mongo + HTTP egress record/replay. Node and Python share the same shape; only prod manifests and worker images differ.

| Suite | Bats file | ShadowTest | Prod worker | Downstream HTTP |
|-------|-----------|------------|-------------|-----------------|
| Node | [`nodejs_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/nodejs_hybrid.bats) | `bats-nodejs-hybrid` | `nodejs-prod-worker` | `user-service-nodejs` / Host `user-service-nodejs.default.internal` |
| Python | [`python_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/python_hybrid.bats) | `bats-python-hybrid` | `python-prod-worker` | `user-service-python` / Host `user-service-python.default.internal` |

Run one suite:

```bash
SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-one FILE=e2e/rabbitmq-ingress/nodejs_hybrid.bats
SKIP_BUILD=1 SKIP_LOAD=1 make test-bats-one FILE=e2e/rabbitmq-ingress/python_hybrid.bats
```

---

## What “hybrid” means

One ShadowTest wires **four concerns** in a single prod worker path:

1. **Ingress** — prod RMQ `orders` → igris-rabbitmq fan-out → three shadow workers  
2. **HTTP egress record/replay** — prod worker HTTP → Kaisel (request+response paired) → Shop; shadows get mocks via Envoy `shop_ext_proc`  
4. **RabbitMQ egress** — shadow Firehose → egress-relay → Beru; candidate intentionally publishes twice  

There is **no** `igris-http` on these fixtures (RMQ-only input). Test 2 that needs HTTP multicast is therefore an intentional skip.

---

## Setup flow (`setup_file`)

```mermaid
flowchart TD
  plat[ensure_platform_ready] --> prod[Deploy prod RMQ Mongo user-service worker]
  prod --> onlyOne[Delete competing language worker]
  onlyOne --> st[Apply ShadowTest CR]
  st --> wait[wait_shadowtest_ready mongo rmq kaisel]
  wait --> beru[wait_local_beru_rollout]
```

Prod stack (default namespace):

- `rmq-prod-broker`, `mongo-prod`
- Downstream stub (`user-service-*`)
- Language worker that consumes `order.created` and performs Mongo + HTTP + RMQ egress

Monarch materializes `shadow-default-<shadowtest>` with control-a / control-b / candidate, igris-rabbitmq, Shop, beru-local, per-role Mongo/RMQ, egress-relay-rabbitmq, and a `KaiselRule` (ingress + egress URLs).

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
  Shadow->>Beru: Mongo OTLP + RMQ Firehose relay
```

HTTP capture uses the dual-branch egress PxL (client `trace_role==1` + server `trace_role==2` scoped by remote client pod). See [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md).

---

## What each `@test` asserts

### 1. HTTP ingress reaches all shadow roles

Misleading name historically — this is **RMQ ingress + HTTP replay**, not HTTP ingress via igris-http.

1. `publish_rmq_order`
2. `wait_kaisel_egress_seed` (skip if the egress seed is not observed)
3. For control-a, control-b, candidate: `assert_worker_http_replay`  
   - log contains `order_id=…`  
   - log contains `http egress via=replay status=200`

### 2. Mongo egress diff is clean (intentional skip)

Skipped when `${SHADOWTEST}-igris` (igris-http) is absent — hybrid fixtures are RMQ-only. Covered elsewhere by `integration/mongo_egress.bats`.

### 3. Candidate Mongo count regression

1. Publish + wait for the Kaisel egress seed  
2. Confirm all three roles processed the order  
3. Wait Beru log for Mongo **egress count regression**  
4. API verdict line: `MISMATCH|1`  

Candidate worker inserts an extra Mongo document (`candidate_n+1`); controls do not — noise-filtered diff-of-diffs still flags the count gap.

### 4. RabbitMQ egress count regression

Same publish/seed/process path; wait Beru log for RabbitMQ egress count regression. Candidate double-publishes on `egress-events` / `order.shipped`.

---

## Assertion cheat sheet

| Signal | Where |
|--------|--------|
| Prod HTTP recorded | Prod worker: `http egress via=record status=200` |
| Shop seeded | Kaisel: `egress recorded ... hash=trace:<id>:POST:<host>:/v1/log` |
| Shadow HTTP replay | Shadow app: `http egress via=replay status=200` |
| Mongo / RMQ regression | beru-local logs + `/api` verdict helpers in bats |

---

## Citations

- Node suite: [`testing/bats/e2e/rabbitmq-ingress/nodejs_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/nodejs_hybrid.bats)
- Python suite: [`testing/bats/e2e/rabbitmq-ingress/python_hybrid.bats`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/e2e/rabbitmq-ingress/python_hybrid.bats)
- Traffic helpers: [`testing/bats/lib/traffic.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/traffic.bash) — `publish_rmq_order`; and [`testing/bats/lib/kaisel.bash`](https://github.com/shadow-diff/monarch/tree/main/testing/bats/lib/kaisel.bash) — `wait_kaisel_egress_seed`
- Node worker N+1 behavior: [`testing/example-apps/nodejs-hybrid-worker/index.js`](https://github.com/shadow-diff/monarch/tree/main/testing/example-apps/nodejs-hybrid-worker/index.js)
- Egress record/replay: [/data-plane/egress-record-replay.md](/data-plane/egress-record-replay.md)
- Bats harness: [/infrastructure/bats-testing-framework.md](/infrastructure/bats-testing-framework.md)
