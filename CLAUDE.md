# CLAUDE.md

@.cursor/rules/ponytail.md
@.cursor/rules/shadow-diff-core.md
@.cursor/rules/shadow-diff-wiki.md
@.claude/rules/shadow-diff-doc-hygiene.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

**Shadow-Diff** is a differential testing framework for Kubernetes. A single `ShadowTest` CR causes the **Monarch** operator to spin up an isolated shadow stack (3 roles: control-a, control-b, candidate) with Envoy sidecars, and wire all traffic through a diff-of-diffs pipeline. Noise is measured as `Diff(control-a, control-b)`; regressions are `Diff(control-a, candidate) − noise`.

## Build & test commands

All commands are run from the relevant service directory unless noted.

### Run all unit tests (from repo root)
```bash
make test-all
```

### Per-service (from `pipeline/<service>/`)
```bash
make build       # compile binary
make test        # go test ./...
make docker-build
```

### Monarch operator (from `pipeline/monarch/`)
```bash
make manifests generate   # regenerate CRD YAML + deepcopy after changing types
make fmt vet              # format and vet
make lint                 # golangci-lint v2
make lint-fix
make test                 # runs setup-envtest then go test ./...
make test-e2e             # Kind-based E2E (creates monarch-test-e2e cluster)
make deploy IMG=monarch:dev   # deploy controller to current kube context
```

### Beru (from `pipeline/beru/`)
```bash
make proto        # regenerate protobuf (requires protoc + plugins)
make test
make docker-build BERU_IMG=beru:dev
```
Beru has no manifests of its own — Monarch deploys `beru-local` into each shadow namespace.

### Single Go test
```bash
go test ./internal/controller/... -run TestRenderEnvoyYAML -v
go test ./internal/v2/report/... -run TestEgressSignature -v
```

### E2E scripts (bats framework under `testing/bats/`)
```bash
make test-bats-integration   # integration suite (mongo egress)
make test-bats-e2e           # full E2E suite (python/nodejs hybrid + http-otel-rmq)
make test-bats               # both suites
```

## Architecture

### Layer stack

```
L0  ShadowTest CR           Monarch reconciler
L1  Capture                 Kaisel (HTTP eBPF) / igris-rabbitmq (AMQP queue bind)
L2  Ingress hub             igris-http (HTTP/TCP multicast) or igris-rabbitmq (AMQP fan-out)
L3  Shadow stack            3× app Deployment + Envoy sidecar + ephemeral deps per role
L4a AMQP egress             egress-relay-rabbitmq (Firehose → Beru)
L4b HTTP egress             Kaisel (request/response pairing → Shop mock store)
L4c DB egress               shadow-soldier (TCP proxy sidecar → Beru egress diff)
L5  Analysis sink           Beru (diff-of-diffs, Postgres + disk WAL) + Shop (per-ShadowTest HTTP egress mock store)
L6  Topology feed           Monarch gRPC :9090 → Tusk BFF (React Flow graph over WebSockets :8082)
```

### Key components

**`pipeline/monarch/`** — Kubebuilder operator (`github.com/shadow-diff/monarch`)  
Reconcile flow: validate → shadow namespace → dependencies → igris → 3× shadow deployments + Envoy ConfigMaps → Shop (always-on) → KaiselRule (ingress + egress) → status patch.  
Shadow namespace is always `shadow-<crNamespace>-<crName>`.  
Key files: `internal/controller/shadowtest_controller.go` (main loop), `shadowtest_envoy.go` (Envoy YAML rendering), `shadowtest_dependencies.go` (dep env injection), `shadowtest_kaisel.go` (KaiselRule), `shadowtest_beru_local.go` (per-ShadowTest beru-local pod).

**`pipeline/beru/`** — L5 analysis sink (`github.com/shadow-diff/beru`)  
Two ports: gRPC `:50051` (Envoy ext_proc + TrafficReporter), HTTP `:8080` (egress/wire ingest, seed, slim trace detail).  
State engine: `internal/v2/engine/` — `TraceRouter` FNV-shards reports by trace ID → `AppendReport` to Bbolt WAL → claimed 8-worker flusher under `pg_advisory_xact_lock` → insert + `EvaluateTraceHistory` + verdict upsert.  
Models in `internal/v2/storage/`; Postgres + WAL in `internal/storage/`. Protocol-specific report builders in `internal/v2/report/`.  
Egress diff ingest: `internal/api/http.go` `handleEgressDiff()` → `FromEgressWithSignature` → `Router.Route`. Producers may supply their own `protocol:operation:target` signature.

**`pipeline/igrises/igris-http/`** — HTTP/TCP multicast hub  
Listens on ports from `/etc/igris/listeners.json` (written by Monarch). Stamps W3C trace context once, fans out async to control-a/b/candidate, returns 202 immediately.

**`pipeline/igrises/igris-rabbitmq/`** — AMQP fan-out  
Consumes the Monarch-declared shadow queue on the prod broker, republishes to 3× shadow RabbitMQ brokers with trace headers.

**`pipeline/kaisel/`** — Self-hosted eBPF HTTP capture (ingress and egress)  
AF_PACKET + socket filter → TCP reassembly → admit/sample. Ingress: HTTP POST to per-ShadowTest igris. Egress: pairs request with response and POSTs the pair to the shadow namespace's Shop. Driven by `KaiselRule` CRs from Monarch.

**`pipeline/shadow-soldier/`** — Database egress capture sidecar (`github.com/shadow-diff/shadow-soldier`)  
Plain-text TCP proxy injected into each shadow role pod alongside Envoy. Monarch rewrites the app's dependency connection strings to `127.0.0.1:<port>`; the sidecar forwards to the real per-role dependency Service and decodes MongoDB / PostgreSQL / Redis / MSSQL wire protocols in passing, POSTing each query to beru-local `/api/v1/egress/diff` on port **8081** (not 8080 — the pod's iptables rules redirect 8080 into Envoy's egress listener). Fail-open: parser errors never break the socket. Only injected when a proxied dependency is declared; RabbitMQ is excluded (covered by egress-relay-rabbitmq).

**`pipeline/tusk/`** — Topology BFF (`github.com/shadow-diff/tusk`)  
Consumes Monarch's `monarch.v1.MonarchStatusService` stream on `:9090`, converts each `ShadowTestStatusUpdate` into a React Flow graph (`pkg/topology`), and fans it out to browsers on `:8082` via `GET /ws/monitor?test=&namespace=`. One upstream gRPC stream serves all clients; the latest graph per ShadowTest is cached so a browser attaching to a converged test renders immediately. Cluster-wide singleton — ships its own manifests in `deploy/`, like Kaisel. The shared wire contract lives in `pipeline/pkg/monarchpb` so Tusk never imports Monarch's operator module.

**`pipeline/shop/`** — Per-ShadowTest HTTP egress mock store  
In-memory mock store deployed by Monarch into each shadow namespace. gRPC ext_proc on `:50051` (Envoy egress replay), HTTP `:8080` (`POST /v1/record_egress` seeding). Mocks keyed by `trace:<traceID>:<METHOD>:<host>:<path>`.

**`pipeline/egress-relay-rabbitmq/`** — AMQP egress relay  
Subscribes to Firehose on each shadow broker, deduplicates (OTel pika double-publish), POSTs to `/api/v1/egress/diff` on Beru.

### Beru-local

Monarch provisions a `beru-local` pod per ShadowTest inside the shadow namespace — this is the only Beru. It always mounts a disk EmptyDir at `/data` for the Bbolt WAL and dead-letter file. `BERU_DB_SECRET` on the manager names the Postgres Secret; Monarch replicates it into each shadow namespace and mounts it via `envFrom`. Diff history in PostgreSQL outlives the ShadowTest; the WAL does not survive pod restart. See `docs/data-plane/beru-postgres-storage.md`.

### Key design patterns

- **Diff-of-diffs**: noise = Diff(control-a, control-b); regression = Diff(control-a, candidate) minus noise.
- **Signature correlation**: Egress ops matched by `protocol:operation:collection` signature (not index), so out-of-order side effects still compare correctly.
- **Re-diff on every arrival**: Every new report re-evaluates the full trace history — late reports are handled automatically.
- **FNV shard routing**: All reports for a given trace ID land on the same `TraceRouter` worker goroutine; no per-trace locking needed.
- **`MONARCH_MODE=dev`**: Must be set on the controller Deployment when running E2E with locally-built `:dev` images (igris, beru, shop, etc.).
- **`failure_mode_allow: true`** on Envoy ext_proc: ingress/egress HTTP requests are never blocked if beru-local is unreachable; reports simply aren't recorded.

### Go workspace

`go.work` ties together 16 modules (9 pipeline services + the shared `pipeline/pkg/*` libraries — `sample`, `s3utils`, `trace`, `replay`, `monarchpb`, `shadowspec` — plus the db-test-app fixture). Run `go build ./...` or `go test ./...` from a module directory, not the repo root. The workspace requires Go 1.26; local toolchains running 1.23 produce `go.work requires go >= 1.26.0` warnings from LSP — these are harmless and do not affect `go build` or `go test`.

### CRD types

`ShadowTest` and `KaiselRule` are defined in `pipeline/monarch/api/v1alpha1/`. After any struct field change run `make manifests generate` from `pipeline/monarch/`. Plain string fields in specs are covered by the existing `*out = *in` deepcopy; only slice/pointer/map fields need explicit deepcopy code.

---

## Custom Rules Enforcement & Verification Loop

### 1. Mandatory Pre-Execution Directives (CRITICAL)
- **YOU MUST ALWAYS** fully read and process all rules within `.claude/rules/` (`ponytail.md`, `shadow-diff-core.md`, `shadow-diff-wiki.md`) before editing any files or writing code. 
- **DO NOT DRIFT:** Never skip documentation out of convenience. You are strictly obligated to update the relevant `index.md` files and specifications synchronously with any structural code modifications.

### 2. Mandatory Verification Step
- Before declaring a task "done" or concluding a turn, you **MUST EXPLICITLY VERIFY** that you have satisfied the rules. Check your work against this criteria:
  1. Did I update or create the required architectural specification file in `docs/`?
  2. Did I incrementally update the sub-directory `index.md` mapping?
  3. Does all updated documentation strictly match the Google OKF v0.1 frontmatter regex layout (`^---[\s\S]*?---`)?

### 3. Automated Post-Task Ledger Constraint
- **DO NOT WRITE TO `docs/log.md` MANUALLY.** - Immediately upon completing a task, you **MUST** run the terminal command below as your final action step to update the chronological ledger:
  ```bash
  make log MSG="'<file_path_or_scope>': <Clear description of what was added/modified>"