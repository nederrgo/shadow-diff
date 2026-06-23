# ROADMAP: Shadow-Diff MVP & Beyond

This roadmap outlines the journey of **Shadow-Diff** from its current highly successful HTTP ingress/egress record-replay state to a **Universal, Multi-Protocol, Zero-Trust Shadowing Platform**.

Our core philosophy is **Pragmatic Isolation**: we leverage native protocols, proxy layers (Envoy), and Go-based classic BPF networks where possible to keep the codebase clean, stable, and highly performant.

---

## Milestone Timeline

```
  Phase 3b & 4a (DONE)      Phase 5a (Sandbox)       Phase 5b (RabbitMQ)       Phase 5c (Egress Diff)     Phase 6 (UI & DB)
+-----------------------+  +--------------------+   +---------------------+   +---------------------+   +-------------------+
|  L7 HTTP Ingress      |  |  Monarch deploys   |   |  Igris AMQP Driver  |   |  Beru Mongo Proxy   |   |  SQLite Storage   |
|  & Egress Record/     |  |  Ephemeral DBs     |   |  (RabbitMQ Ingress) |   |  & AMQP Egress Diff |   |  & HTML Dashboard |
|  Replay (100% Green)  |  |  & Overrides Env   |   |                     |   |                     |   |                   |
+-----------+-----------+  +---------+----------+   +---------+-----------+   +---------+-----------+   +---------+---------+
            |                        |                        |                         |                        |
            +----------------------->+----------------------->+------------------------>+----------------------->| MVP COMPLETE!
```

---

## Completed Milestones (2026 Baseline)

### Phase 1 & 2: Core Platform & Diff Engine
*   **Monarch Operator:** Orchestrates shadow namespaces, injects Envoy sidecars, and synchronizes configurations.
*   **Beru Diff-of-Diffs:** Implemented gRPC-based ingress collection with custom noise-filtering logic (Control-A vs. Control-B dynamically flags noise; Control-A vs. Candidate identifies regressions).

### Phase 3a & 3b: Ingress Multicast & Capture
*   **Igris Multicaster:** Refactored into a high-performance, driver-based engine supporting atomic HTTP and raw TCP streaming.
*   **Siphon Capture Agent:** Implemented a pure Go, node-level DaemonSet utilizing **Classic BPF (`AF_PACKET`)** instead of complex raw eBPF bytecode. Proven to compile and run seamlessly inside `kind`.

### Phase 4a: Egress Record & Replay
*   **4a.1 - Strict Shadow Replay:** Envoy egress proxying configured via `HTTP_PROXY`. Intercepts calls to downstream APIs and executes strict hash-matching against Beru's MockStore. Returns **599 Egress Regression** on a mismatch.
*   **4a.2 - Auto-Recording:** Siphon automatically sniffs production egress calls, pairs requests and responses, and asynchronously posts them to Beru's `/v1/record_egress` endpoint to populate the MockStore on-the-fly.

---

## Upcoming Milestones: The Road to v1.0

### Phase 5a: Ephemeral Dependency Sandboxing (Current Target)
**Goal:** Prevent shadow workloads from mutating real-world production databases by dynamically deploying isolated environments on-demand.

*   [x] **CRD Extensions:** Add `spec.dependencies` to `ShadowTestSpec` to allow users to declare required backing services (e.g., Redis, MongoDB).
*   [x] **Ephemeral DB Provisioner:** Update Monarch to deploy single-replica instances of requested databases inside the shadow namespace, using standard open-source Docker images (e.g., `redis:7-alpine`).
*   [x] **Dynamic Environment Injection:** Monarch overwrites connection string environment variables (e.g., `REDIS_HOST`, `MONGO_URL`) on the shadow application containers, redirecting them to the local shadow services.
*   [x] **Readiness Gate Update:** Monarch blocks `ShadowTest.Status.Phase: Ready` until all ephemeral dependency pods have `AvailableReplicas > 0`.
*   [x] **E2E verification:** `./testing/scripts/e2e-dependency-test.sh` (manifests under `testing/scripts/manifests/dependency-e2e/`).

---

### Phase 5b: Message Broker Ingress (The RabbitMQ Trigger) — Done
**Goal:** Support asynchronous, message-driven shadow triggers natively (Siphon bypassed for ingress).

*   [x] **`igris-rabbitmq` module:** Consumes Monarch’s prod shadow queue, injects `x-shadow-trace-id`, `ExchangeDeclare` on shadow brokers, multicasts to three role-specific brokers.
*   [x] **Monarch queue orchestration:** One-time `QueueDeclare` with `x-max-length` / `x-overflow` / `x-expires`; `status.amqpQueueName` gate; delete on ShadowTest removal.
*   [x] **CRD:** `spec.inputs[].driver: rabbitmq_message` + `amqp` block; `spec.igrisRabbitmq`; AMQP-only ShadowTests (no HTTP Igris / no Siphon ingress).
*   [x] **E2E:** `./testing/scripts/e2e-rabbitmq-test.sh` (manifests under `testing/scripts/manifests/rabbitmq-e2e/`).

---

### Phase 5c: Multi-Protocol Egress Diffing
**Goal:** Expand Beru's diffing engine to analyze database queries and outbound message queues, moving past purely HTTP-based validation.

*   [ ] **MongoDB Egress Diffing:** Configure the shadow pods' Envoy sidecars with the native `mongo_proxy` network filter. Envoy decodes binary MongoDB payloads and streams them as structured query logs to Beru via **gRPC Access Log Service (ALS)**. Beru diffs the query sequence.
*   [ ] **RabbitMQ Egress Diffing:** Beru registers as an active consumer on the Shadow RabbitMQ broker's outbound exchanges to capture, parse, and diff published AMQP messages from the shadow pods.

---

### Phase 6: Persistence & The Beru Dashboard
**Goal:** Productize the platform by migrating from transient in-memory stores to structured, persistent storage with a user-friendly UI.

*   [ ] **Beru SQLite Storage:** 
    *   Integrate a local, zero-dependency **SQLite** database inside the Beru container.
    *   Store all Trace IDs, match metrics, and detailed regression diffs persistently.
*   [ ] **The Beru HTML Dashboard:**
    *   Expose a lightweight, lightning-fast web dashboard hosted on Beru's `:8080/dashboard` port using Go’s standard `html/template` engine (preventing Node/React dependency bloat).
    *   **Features:**
        *   Interactive timeline of past shadow runs.
        *   Filterable view of matches vs. mismatches.
        *   Side-by-side JSON diffs highlighting exactly which field failed (e.g., `body.price`).
        *   **"Approve Intentional Change"** button to dynamically register dynamic fields as noise in Beru’s configuration.

---

## SaaS-Grade Platform Guarantees

1.  **Strict Isolation:** Production data is protected via network isolation and ephemeral sandboxes.
2.  **Zero-Touch Production:** No container restarts or code modifications are required for production applications.
3.  **Graceful Degeneracy:** Production traffic always has priority. Siphon drops capture events if the kernel buffer is pressured, ensuring zero impact on live users.