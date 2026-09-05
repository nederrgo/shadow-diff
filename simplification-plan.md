---
type: Engineering Plan
title: Shadow-Diff Simplification Plan
description: Vet findings and a phased fix plan for design and code simplification across the Shadow-Diff pipeline — shared trace/Beru-client packages, controller boot-gate extraction, dead code removal, and SQLite doc drift.
resource: https://github.com/shadow-diff/monarch
tags: [vet, refactor, simplification, tech-debt, monarch, beru, kaisel, igris, shop]
timestamp: 2026-09-05T20:30:00Z
---

# Shadow-Diff — Simplification Plan

**Scope**: full project (10 Go modules + `pipeline/the-system`)
**Date**: 2026-09-05
**Analysed**: 22,642 non-test Go lines (of which ~1,500 generated), 15,430 test lines, 1 TS/React app

## Executive summary

This is a disciplined codebase. `go vet` is clean across every module, there is exactly **one** `TODO`
in non-generated code (and it is kubebuilder scaffold), error handling is deliberate rather than
sloppy, and the `ponytail:` convention means known ceilings are already written down at the call site
instead of being discovered later. There is no rot to clean up here.

What there *is* is **accidental duplication left behind by the record/replay pivot**, and one
structural repetition in the Monarch reconciler. Five copies of the same W3C parser, three copies of
the same Beru HTTP client, two packages both called `storage` in the same module, and a vestigial
`internal/v2/` namespace with no v1 to distinguish it from. None of it is broken. All of it is code
that has to be read five times to learn one fact.

- 🔴 Critical: **0**
- 🟠 Worth doing: **6** (duplication, controller boot gates, doc drift)
- 🟡 Housekeeping: **5** (dead code, gofmt, logging, unused TS exports)
- ✅ Clean: `go vet` all modules, no TODO debt, no hardcoded secrets, status-writer design, `pkg/replay` sharing

Net: roughly **400 lines removable** with no behaviour change, plus ~120 lines of reconciler
boilerplate collapsible into a declarative table.

---

## Findings

### 1. Five implementations of `ParseTraceparent` 🟠

There is already a shared module for this — `pipeline/pkg/trace` — and four services do not use it.

| File | Lines | Notes |
|------|-------|-------|
| `pipeline/pkg/trace/w3c.go` | 70 | the canonical one, named constants, `GenerateTraceID`, `FormatTraceparent` |
| `pipeline/beru/internal/trace/trace.go` | 55 | byte-identical parser + `TraceIDFromMap` |
| `pipeline/shop/internal/trace/trace.go` | 54 | byte-identical to beru's, magic numbers instead of constants |
| `pipeline/egress-relay-rabbitmq/internal/trace/w3c.go` | 33 | same parser, returns span id too |
| `pipeline/shadow-soldier/internal/parsers/trace.go` | — | protocol-specific extraction, keep |

`beru` and `shop` are the same file twice, down to the comment. The trace id is *the* correlation key
for the entire platform — five parsers that must agree is five places a version-byte or casing rule
can drift apart silently, and the failure mode is not a crash, it's a trace that quietly never
correlates.

**Fix**: promote `TraceIDFromMap` (the Envoy `HeaderMap` variant, currently duplicated in beru and
shop) into `pipeline/pkg/trace` behind a small interface so the shared module doesn't take a hard
dependency on go-control-plane; delete the three private copies. Keep
`shadow-soldier/internal/parsers/trace.go` — it extracts trace ids out of MongoDB comments and SQL
comments, which is a genuinely different job.

- Effort **S** · Risk **low** (parser bodies are identical; existing tests cover it) · Removes ~110 lines

### 2. Three implementations of the Beru report client 🟠

| File | Lines | Retry? | Typed error? | `ShadowTestName`? | URL contract |
|------|-------|--------|--------------|-------------------|--------------|
| `pipeline/shadow-soldier/internal/beru/client.go` | 109 | yes, policy-aware | yes | yes | base URL + path |
| `pipeline/shop/internal/beru/client.go` | 99 | **no** | no | yes | base URL + path |
| `pipeline/egress-relay-rabbitmq/internal/beru/client.go` | 60 | **no** | no | **no** | **full URL** |

The `PostReport` bodies are the same code three times. The divergences are the interesting part, and
they are all accidental:

- Only shadow-soldier has the retry policy — and that policy encodes a platform-wide invariant
  ("Beru does not deduplicate, so never retry a timeout"). Shop and egress-relay post fire-and-forget
  and log a warning, so a transient connection reset there is a silently lost report.
- egress-relay takes a **full** URL where the other two take a base URL and append the path. That
  single inconsistency is the entire reason `BERU_EGRESS_DIFF_PATH` exists as a config knob
  (`egress-relay-rabbitmq/internal/config/config.go:44`) — an env var whose only job is to paper over
  a client-API disagreement.
- egress-relay never sets `ShadowTestName`, so its reports land on Beru's
  `DefaultShadowTestName()` fallback. Correct today only because beru-local is per-test and Monarch
  sets `BERU_SHADOW_TEST_NAME`. It is correctness-by-coincidence.

**Fix**: one `pipeline/pkg/beruclient` module holding `Report` (superset struct — `Signature` and
`ShadowTestName` both `omitempty`), `Client`, `PostReport`, the typed `StatusError`, and the
`Retryable(err)` policy with its comment. Payload builders stay with their owners:
`BuildHTTPEgressPayload` in shop, `BuildPayload` in shadow-soldier — they encode
protocol-specific knowledge, not transport. Delete `BERU_EGRESS_DIFF_PATH`.

Shop and egress-relay get the retry policy for free, which is the actual win. The cost: one more
module in `go.work`, and a shared struct that carries fields two of its three callers don't set.

- Effort **M** · Risk **low-medium** (behaviour changes for shop/egress-relay — they start retrying) · Removes ~150 lines, deletes one config knob

### 3. `beru/internal/v2/` is vestigial, and two packages are both named `storage` 🟠

There is no v1. Nothing in the module references one — the only `/v1/` hits are HTTP paths in test
fixtures and the generated gRPC package. Meanwhile two live packages share a name:

```
pipeline/beru/internal/storage/       → Postgres + Bbolt WAL   (the backend)
pipeline/beru/internal/v2/storage/    → models + Repository    (the domain types)
```

Every file that touches both has to alias one (`v2storage "…/internal/v2/storage"` in
`internal/api/http.go:14` and `internal/v2/engine/router.go:14`). A reader has to hold "which
storage?" in their head for the whole file.

**Fix**: flatten and rename, mechanically:

```
internal/v2/diff     → internal/diff
internal/v2/engine   → internal/engine
internal/v2/report   → internal/report
internal/v2/storage  → internal/model      ← the rename that does the real work
internal/storage     → internal/storage    (unchanged)
```

`internal/model` because what lives there is `RawReport`, `VerdictState`, `TraceSummary` and a
`Repository` interface — those are the domain model, not a storage engine. Pure `gofmt -r` / import
rewrite, zero logic change.

- Effort **S** · Risk **very low** · Removes a naming tax paid on every read of the module

### 4. The Monarch reconciler repeats one boot-gate shape nine times 🟠

`shadowtest_controller.go:179-294` and `reconcileIngressRelays` at `:326-411` are ~200 lines built
almost entirely out of this block, once per component:

```go
if err := r.reconcileX(ctx, st, shadowNS); err != nil {
    _ = r.patchBootStatus(ctx, st, phaseFailed, err.Error(), shadowNS, step, boot)
    return ctrl.Result{}, err
}
ready, reason, err := r.xReady(ctx, shadowNS)
if err != nil { return ctrl.Result{}, err }
boot.XReady = ready
if !ready {
    if reason.terminal { return r.markBootFailed(ctx, st, shadowNS, reason.message, boot) }
    _ = r.patchBootStatus(ctx, st, phaseProgressing, "waiting for X", shadowNS, step, boot)
    return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
```

Nine instances. `RequeueAfter: 5 * time.Second` appears **12** times as a bare literal. Seven of the
readiness functions are one-line wrappers around the same `deploymentBootReady`. And the
egress-relay stack is reconciled from two separate places in `reconcileIngressRelays`
(`:352-367` and `:393-408`) with identical bodies.

The deeper point: the **ordering** of these gates is the single most important thing about this
reconciler — "Shop before ABC so egress mocks are ready when shadow pods start", "sinks before
KaiselRule before AMQP bind" — and today that ordering exists only as the sequence of statements in
a 260-line function, explained in prose comments. It is the one part of the design that most wants to
be data.

**Fix**: a `bootGate` descriptor and a driver loop.

```go
type bootGate struct {
    name      string
    step      enginev1alpha1.BootStep
    waitMsg   string
    skip      bool                                    // mode / spec gating, evaluated up front
    reconcile func(context.Context) error
    ready     func(context.Context) (bool, workloadWaitReason, error)
    record    func(*enginev1alpha1.ComponentStatus, bool)
}
```

`Reconcile` then builds the gate list per mode — which makes the record-vs-replay split and the
ordering rationale readable in one screen — and runs one loop that owns the patch/requeue/terminal
logic exactly once. Hoist the requeue interval to a named `bootRequeueInterval` constant.

Trade-off, stated honestly: a table-driven gate list is one indirection further from the
straight-line code, and a reader chasing a specific failure has to look in two places instead of
one. The up-side is that the ordering contract becomes a list you can read and diff, the
patch-status-and-requeue logic exists once instead of nine times, and adding a component stops being
a copy-paste. Worth it at nine instances; it would not have been at three.

- Effort **M-L** · Risk **medium** — this is the reconcile loop; do it behind the existing controller tests, one gate at a time · Collapses ~200 lines to ~80

### 5. Seven docs contradict the code on SQLite 🟠

`docs/data-plane/beru-postgres-storage.md:19` is correct: *"PostgreSQL is the sole database engine…
There is no SQLite path."* Six other documents disagree with it.

| File | Refs | What it claims |
|------|------|----------------|
| `docs/control-plane/platform-bootstrap-and-shadowtest-lifecycle.md` | 4 | a whole storage-mode table with "**tmpfs SQLite** (default)" |
| `docs/refactor/BERU_STATE_MACHINE_TARGET.md` | 3 | design doc targeting "SQLite in WAL mode" |
| `docs/data-plane/beru-system-removal.md` | 2 | "beru-local stays on tmpfs SQLite" |
| `docs/architecture/ARCHITECTURE.md` | 1 | line 221 — "tmpfs SQLite by default" |
| `docs/control-plane/monarch-controller.md` | 1 | "→ SQLite `raw_reports`" |
| `docs/data-plane/shadow-soldier.md` | 1 | "the SQLite grouping key" |

The code is unambiguous — `pipeline/beru/internal/storage/open.go` says *"Missing DB_HOST / DB_USER /
DB_NAME fails the boot — there is no SQLite fallback"*, and `postgres.go` refuses a partially
configured DSN on the grounds that *"a durability backend that silently is not there is worse than a
boot failure."*

Related drift in the same tree:

- `CLAUDE.md`'s L0–L6 layer stack and its reconcile-flow summary describe the pre-pivot synchronous
  live-traffic model. No mention of record/replay, S3, sessions, the UI projection tables, or
  `pg_notify`.
- `docs/architecture/ARCHITECTURE.md` — the `## Monorepo layout` table header is corrupted with a
  stray `image.png` glued to it.
- `docs/architecture/ARCHITECTURE-OLD.md` (421 lines) is a superseded copy sitting next to the live
  one, and it is the *more* confident of the two about the old design.

**Fix**: three separate passes, because they are three different jobs. (a) Delete every SQLite
mention, pointing at `beru-postgres-storage.md` as the single source. (b) Rewrite the `CLAUDE.md`
layer stack around the two modes. (c) Move `ARCHITECTURE-OLD.md` and `BERU_STATE_MACHINE_TARGET.md`
into an explicitly historical folder with a header saying so, or delete them — a design doc for a
target that was reached differently is worse than no doc.

- Effort **M** · Risk **none** · Removes the most expensive kind of drift: docs that disagree with each other

### 6. Four copies of `envOr` 🟡

`igris-rabbitmq/internal/config/config.go:89`, `igris-http/internal/config/config.go:274`,
`tusk/cmd/tusk/main.go:107`, `shop/cmd/shop/main.go:145` — plus `firstEnv` in igris-http.

Genuinely trivial, and a shared `pkg/envconf` for a three-line function is arguably worse than the
duplication. Bundle it with #2 if a shared module is being created anyway; otherwise leave it.

- Effort **S** · Risk **none** · Judgement call, not a defect

### 7. Dead code 🟡

`deadcode` output, verified by hand (kaisel needs `GOOS=linux`):

| File | Symbol | Notes |
|------|--------|-------|
| `beru/internal/storage/names.go:4` | `ShadowTestNameFromMetadata` | whole file is this one function |
| `beru/internal/v2/diff/diff.go:412` | `hasPayloadRegression` | thin wrapper over `payloadRegressions` |
| `monarch/internal/controller/shadowtest_helpers.go:255` | `hasMongoDependency` | `isMongoDependency` is used; the plural isn't |
| `kaisel/internal/export/exporter.go:73` | `Exporter.Dropped` | see below |
| `igris-http/internal/config/config.go:296` | `validateTargetHost` | |
| `igris-http/internal/config/config.go:312` | `splitHostPortOptional` | only caller is `validateTargetHost` |
| `igris-http/internal/driver/http/http.go:90` | `StopAccepting` (package-level) | the `*Driver` method of the same name **is** used |

`Exporter.Dropped` deserves a moment rather than a delete. `exporter.go:166` says
*"ponytail: drop on full queue; upgrade path = metric + larger queue"* — and `Dropped()` is exactly
the accessor that upgrade path needs, written and then never wired to a Prometheus gauge. Kaisel
already exports `kaisel_ebpf_gate_tier`, so the registry is right there. **Wire it up rather than
delete it**: a silent drop counter that nothing reads is the one flavour of dead code that costs you
an incident.

- Effort **S** · Risk **very low** · Removes ~60 lines, adds one gauge

### 8. `gofmt` is not clean 🟡

23 files unformatted, spread across beru (13), egress-relay (3), igris (3), pkg (2), shop (1),
tusk (1). Notably `egress-relay-rabbitmq/internal/beru/client.go` has misaligned struct fields — a
hint it was hand-copied rather than generated from the original.

**Fix**: `gofmt -w`, then add a `make fmt-check` to CI so this stops recurring. Do it as its own
commit before any refactor above, so the real diffs stay readable.

- Effort **S** · Risk **none**

### 9. Two logging libraries 🟡

Six files still on stdlib `log` while everything else is on `log/slog` (monarch correctly uses
controller-runtime's logger):

`egress-relay-rabbitmq/{cmd,internal/consumer}`, `igris-rabbitmq/{cmd,internal/multicast}`,
`shop/internal/replay/mockstore.go`, `beru/internal/v2/engine/router.go`.

The two AMQP services are the ones that never got the `slog` treatment. It matters for more than
tidiness: `consumer.go:149` logs a failed Beru post as an unstructured `log.Printf`, so the one
signal that says "an egress report was lost" is not queryable by trace id.

- Effort **S** · Risk **none**

### 10. Frontend unused exports 🟡

`npx knip` on `pipeline/the-system` — clean apart from 7 unused exports, no unused files or
dependencies:

`NODE_POSITIONS` (`src/components/layoutPositions.ts:4`), `statusBorderClass`
(`src/components/nodes/nodeStyles.ts:4`), `buildShadowTestDoc` (`src/lib/shadowTestYaml.ts:5`), and
types `DependencyKind`, `InputDriver`, `NodeType`, `TopologyEdge`.

`buildShadowTestDoc` is the interesting one — an exported YAML builder nothing calls, next to a
`ShadowTestForm` that emits YAML. Worth a look before deleting: it may be the better implementation
that lost.

- Effort **S** · Risk **very low**

---

## Two things worth *not* changing

**The status-writer design.** `patchStatusCore` + composable `statusMutator`s
(`shadowtest_resources.go:218-363`) looks like five wrappers that could be collapsed. Leave it. The
mutators are named for what they mean (`statusBase`, `statusBoot`, `statusExtras`), the
DeepEqual-skip and publish-after-write ordering are subtle and correct, and the wrappers are the
readable names at the call sites. This is the good kind of layering.

**The `ponytail:` markers.** 20 of them across the codebase. They read like TODOs and they are not —
they are documented ceilings at the point where the ceiling bites (`sql.go:12`: *"naive keyword scan.
Ceiling — CTEs report…"*). Do not sweep them.

The 21 `_ = r.patchBootStatus(...)` calls in the controller are also deliberate: a failed status
patch shouldn't mask the actual reconcile error. Worth one comment saying so at the top of the
reconciler rather than 21 individual justifications — but not worth changing.

---

## Plan

Ordered so that every phase lands on a clean base and no phase's diff is polluted by another's.

### Phase 0 — make diffs readable (half a day)
1. `gofmt -w` the 23 files. One commit, nothing else in it.
2. Add `fmt-check` to `make test-all` so it stays clean.
3. Delete the six dead symbols in finding #7; **wire `Exporter.Dropped` to a
   `kaisel_export_dropped_total` counter** instead of deleting it.

### Phase 1 — collapse the duplication (2–3 days)
4. Promote `TraceIDFromMap` into `pipeline/pkg/trace`; delete beru's, shop's and egress-relay's
   private trace packages. Run each module's tests.
5. Create `pipeline/pkg/beruclient` with the typed error and retry policy; migrate shadow-soldier
   (no behaviour change), then shop, then egress-relay. Delete `BERU_EGRESS_DIFF_PATH`. Set
   `ShadowTestName` on the AMQP path.
6. Move the six stdlib-`log` files to `slog`, and give `consumer.go:149` a structured trace-id field.

### Phase 2 — rename, no logic (half a day)
7. Flatten `beru/internal/v2/*` and rename `v2/storage` → `internal/model`. Mechanical import
   rewrite, single commit, `make test` per module. Do this *after* Phase 1 so the trace/client moves
   don't collide with it.

### Phase 3 — the reconciler (3–4 days, behind tests)
8. Extract `bootGate` and the driver loop. One gate migrated per commit, existing controller and
   record-mode tests green at each step. Start with `beru-local` (simplest, no skip condition), end
   with the shadow-roles gate (the only one returning a map).
9. De-duplicate the two egress-relay call sites in `reconcileIngressRelays`.
10. Name the requeue interval; delete the 12 literals.

### Phase 4 — docs (1–2 days)
11. Purge SQLite from the six drifted docs; make `beru-postgres-storage.md` the single source.
12. Rewrite the `CLAUDE.md` layer stack and reconcile-flow summary for record/replay. Fix the
    corrupted table header in `ARCHITECTURE.md`.
13. Retire `ARCHITECTURE-OLD.md` and `BERU_STATE_MACHINE_TARGET.md` into a dated historical folder,
    or delete them.
14. Regenerate the affected `index.md` files per the doc-hygiene rule, and `make log` each pass.

### Deferred
- `envOr` consolidation (#6) — only if Phase 1 creates a natural home for it.
- Frontend unused exports (#10) — investigate `buildShadowTestDoc` before deleting.

## Consequences

Phases 0–2 are close to free: mechanical, test-covered, and they remove ~320 lines while making the
trace-parsing and Beru-reporting contracts single-sourced. Shop and egress-relay quietly become more
reliable because they inherit a retry policy someone thought hard about.

Phase 3 is the one with real risk. It touches the reconcile loop, and a bug there does not surface as
a failing test — it surfaces as a ShadowTest that hangs in `Progressing` on someone else's cluster
next month. The mitigation is the per-gate commit discipline, and the honest alternative is to skip
it: nine copies of a working block are ugly but not dangerous. Do it because the *ordering* deserves
to be data, or don't do it at all — a half-migrated gate list would be worse than either.

Phase 4 costs nothing technically and is probably the highest-value item on the list. Six documents
disagreeing with a seventh about whether the database is SQLite is the kind of thing that costs a new
engineer half a day and an on-call engineer an hour at the worst possible time.
