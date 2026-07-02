# CLAUDE.md

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
kubectl apply -f deploy/   # creates beru-system ns + Deployment + Service
```

### Single Go test
```bash
go test ./internal/controller/... -run TestRenderEnvoyYAML -v
go test ./internal/otlp/... -run TestExport -v
```

### E2E scripts (from repo root or `testing/scripts/`)
```bash
testing/scripts/e2e-rmq-mongo-test.sh          # full RMQ + Mongo pipeline
testing/scripts/e2e-pipeline-test.sh           # HTTP ingress
testing/scripts/e2e-rabbitmq-egress-test.sh    # AMQP egress diff
SKIP_BUILD=1 SKIP_LOAD=1 testing/scripts/e2e-rmq-mongo-test.sh  # fast re-run
```

## Architecture

### Layer stack

```
L0  ShadowTest CR           Monarch reconciler
L1  Capture                 Siphon (HTTP via Pixie eBPF) / igris-rabbitmq (AMQP queue bind)
L2  Ingress hub             igris-http (HTTP/TCP multicast) or igris-rabbitmq (AMQP fan-out)
L3  Shadow stack            3× app Deployment + Envoy sidecar + ephemeral deps per role
L4a AMQP egress             egress-relay-rabbitmq (Firehose → Beru)
L4b HTTP egress             Recorder (Pixie egress OTLP → Beru mock store)
L5  Analysis sink           Beru (diff-of-diffs, SQLite, dashboard, mock replay)
```

### Key components

**`pipeline/monarch/`** — Kubebuilder operator (`github.com/shadow-diff/monarch`)  
Reconcile flow: validate → shadow namespace → dependencies → igris → 3× shadow deployments + Envoy ConfigMaps → Recorder (if `spec.recordAndReplay`) → PixieStreamRule → status patch.  
Shadow namespace is always `shadow-<crNamespace>-<crName>`.  
Key files: `internal/controller/shadowtest_controller.go` (main loop), `shadowtest_envoy.go` (Envoy YAML rendering), `shadowtest_dependencies.go` (dep env injection), `shadowtest_siphon.go` (PixieStreamRule), `shadowtest_beru_local.go` (per-ShadowTest beru-local pod).

**`pipeline/beru/`** — L5 analysis sink (`github.com/shadow-diff/beru`)  
Three ports: gRPC `:50051` (Envoy ext_proc + TrafficReporter), OTLP gRPC `:4317`, HTTP `:8080` (REST API, dashboard, egress diff).  
State engine: `internal/v2/engine/` — `TraceRouter` FNV-shards reports by trace ID → single goroutine per trace → `AppendReport` (SQLite) → `EvaluateTraceHistory` → `SaveDiffVerdict` → `mirrorLegacyLogs`.  
SQLite models in `internal/v2/storage/`. Protocol-specific report builders in `internal/v2/report/`.  
OTLP MongoDB path: `internal/otlp/server.go` `Export()` → `isMongoSpan()` → `routeMongoSpan()` → `FromMongoEgress` → `Router.Route`.

**`pipeline/igrises/igris-http/`** — HTTP/TCP multicast hub  
Listens on ports from `/etc/igris/listeners.json` (written by Monarch). Stamps W3C trace context once, fans out async to control-a/b/candidate, returns 202 immediately.

**`pipeline/igrises/igris-rabbitmq/`** — AMQP fan-out  
Consumes the Monarch-declared shadow queue on the prod broker, republishes to 3× shadow RabbitMQ brokers with trace headers.

**`pipeline/siphon/`** — Pixie ingress bridge  
Receives compressed OTLP gRPC from pixie-stream-bridge on `:4317`, parses span attributes → `HTTPRecord`, POSTs to igris-http.

**`pipeline/recorder/`** — Prod HTTP egress recorder  
Accepts Pixie egress OTLP on `:4317`, filters by `recordAndReplay.json`, POSTs `RecordPayload` to Beru's `/v1/record_egress`.

**`pipeline/egress-relay-rabbitmq/`** — AMQP egress relay  
Subscribes to Firehose on each shadow broker, deduplicates (OTel pika double-publish), POSTs to `/api/v1/egress/diff` on Beru.

### Pixie integration

`PixieStreamRule` CR is reconciled by Monarch → the **pixie-stream-bridge** host process (`testing/scripts/pixie-stream-bridge.sh`) polls rules and runs `px run -f <pxl>` → Pixie emits OTLP → Siphon (ingress) or Recorder (egress) or beru-local OTLP port (MongoDB).  
PxL templates live in `testing/scripts/manifests/pixie-bridge/configmap.yaml`.  
Rendering helpers: `testing/scripts/lib/pixie-bridge.sh`.

### Beru-local

When `spec.beruGRPCAddress` is unset, Monarch provisions a per-ShadowTest `beru-local` pod inside the shadow namespace. It uses an **in-memory EmptyDir** for SQLite — all diff state is lost on pod restart. The prod Beru in `beru-system` uses a persistent volume.

### Key design patterns

- **Diff-of-diffs**: noise = Diff(control-a, control-b); regression = Diff(control-a, candidate) minus noise.
- **Signature correlation**: Egress ops matched by `protocol:operation:collection` signature (not index), so out-of-order side effects still compare correctly.
- **Re-diff on every arrival**: Every new report re-evaluates the full trace history — late OTLP spans are handled automatically.
- **FNV shard routing**: All reports for a given trace ID land on the same `TraceRouter` worker goroutine; no per-trace locking needed.
- **`MONARCH_MODE=dev`**: Must be set on the controller Deployment when running E2E with locally-built `:dev` images (igris, beru, recorder, etc.).
- **`failure_mode_allow: true`** on Envoy ext_proc: ingress/egress HTTP requests are never blocked if beru-local is unreachable; reports simply aren't recorded.

### Go workspace

`go.work` ties together 9 modules. Run `go build ./...` or `go test ./...` from a module directory, not the repo root. The workspace requires Go 1.26; local toolchains running 1.23 produce `go.work requires go >= 1.26.0` warnings from LSP — these are harmless and do not affect `go build` or `go test`.

### CRD types

`ShadowTest` and `PixieStreamRule` are defined in `pipeline/monarch/api/v1alpha1/`. After any struct field change run `make manifests generate` from `pipeline/monarch/`. Plain string fields in specs are covered by the existing `*out = *in` deepcopy; only slice/pointer/map fields need explicit deepcopy code.
