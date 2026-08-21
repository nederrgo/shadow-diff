---
type: Audit Report
title: Beru Analysis Sink Bug and Correctness Audit
description: Consolidated correctness findings for pipeline/beru from a local service review (2026-08-20). Severity-ranked; evidence paths point at engine, WAL, and HTTP/ext_proc sources. Checklist tracks remediation; review decisions are the source of truth for follow-up work.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/beru
tags: [audit, bugs, data-plane, beru, wal, postgres, diff-of-diffs, projection, ext_proc]
timestamp: 2026-08-20T20:45:00Z
---

# Beru Analysis Sink Bug and Correctness Audit

Read-only review of `pipeline/beru` (ingest, TraceRouter, Bbolt WAL, Postgres flush/evaluate, diff-of-diffs, UI projection) on 2026-08-20. Findings below track remediation with checkboxes (`[x]` = fixed, `[ ]` = open). This file is the **source of truth** for agreed follow-ups — fix one by one and check them off here.

Related specs: [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md), [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md), [/control-plane/tusk-bff.md](/control-plane/tusk-bff.md).

---

## Summary

| Severity | Count | Fixed |
| -------- | ----- | ----- |
| High     | 2     | 2     |
| Medium   | 2     | 0     |
| Low      | 2     | 0     |
| Deferred | 2     | —     |

**Highest-priority fixes:** H1 baseline signature buckets → H2 WAL-on-accept → M1 ingest idempotency → M2 projection hard-fail.

### Progress checklist

- [x] **H1** — Baseline A↔B uses signature-bucket compare (like C↔A); still void on structural miss
- [x] **H2** — WAL append on HTTP/gRPC accept path so 202 means local-WAL durable
- [ ] **M1** — Ingest idempotency via WAL seq / `ingest_id` (`ON CONFLICT DO NOTHING`)
- [ ] **M2** — Kill projection soft success; same tx as verdict; fix split `SaveDiffVerdict`
- [ ] **L1** — Log ext_proc normalize / `FromHTTPIngress` failures (still CONTINUE)
- [ ] **L2** — Document or extend hardcoded Mongo metadata strip list (small)
- [ ] **D1** — WAL 1 GiB drop-head *(deferred — not caring for now)*
- [ ] **D2** — Seed `/api/v1/debug/seed-reports` auth *(deferred — OK for now)*

### Explicit keep (no change)

| Topic | Call |
| ----- | ---- |
| Postgres required (no SQLite) | Keep |
| Full re-diff every flush | Keep (per-signature volume stays small) |
| UI projection tables (`traces`, `diff_reports`) | Keep for Tusk; fix consistency via M2 |
| Ingress Envoy `failure_mode_allow: true` → Beru | Keep (not a Shop problem; ops/alert if needed) |
| Seed endpoint network isolation | Keep for now (D2) |

---

## High

### H1. Baseline A↔B requires global egress order; candidate does not

- [x] Fixed

**Class:** correctness / false voids  
**Evidence:** `internal/v2/diff/diff.go` (`verifyBaseline` egress sequence vs `compareSignature` signature buckets)

**Bug / asymmetry:** Control-a vs control-b must match the full ordered egress signature playlist. Candidate vs control-a buckets by signature and compares counts + per-index payloads. Cross-signature reorder between controls voids the whole trace (`VOIDED_BASELINE_DIVERGENCE`) even when the candidate would score cleanly. Candidate reorder across signatures is already allowed (`TestEvaluateTraceHistory_outOfOrderProtocols_match`).

**Impact:** Flaky control-b scheduling/order noise voids useful candidate results. Product signal for “total order between different ops” is inconsistent (strict for controls, ignored for candidate).

**Agreed fix:**

1. Change baseline egress checks to the **same signature-bucket / count rules** as C↔A.
2. Keep baseline failures as **void** (do not emit MATCH/MISMATCH for A↔B).
3. Payload A↔B remains noise for A→C, not a void reason.
4. Accept: cross-signature reorder is not a regression signal for controls or candidate.
5. Tests: A/B reorder → not void; A/B per-signature count miss → still void.

---

### H2. HTTP 202 before local WAL durability

- [x] Fixed

**Class:** durability semantics / ops  
**Evidence:** `internal/api/http.go` (`handleEgressDiff`, `handleWireIngest`); `internal/v2/engine/router.go` (`Route` → sync `AppendReport`); `internal/storage/wal.go` (`AppendReport` → `appendWAL`)

**Bug:** Egress/wire handlers called `Router.Route` (enqueue) then returned **202**. WAL append ran later on a worker. Client believed accept; process death before `appendWAL` lost the report. EmptyDir WAL need not survive pod reschedule (accepted).

**Impact:** Producers (shadow-soldier, egress-relay) could treat 202 as durable when only the in-memory queue accepted the report.

**Agreed fix:**

1. Move **WAL write onto the accept path** so 202 means “on local Bbolt WAL.”
2. Postgres flush stays async (keep ingest off the DB hot path after durable enqueue).
3. Pod/EmptyDir loss dropping WAL remains acceptable.

**Done:** `Route` sync-appends and returns error; HTTP egress/wire/seed → **503** on WAL fail; TrafficReporter → gRPC `Unavailable`; ext_proc still CONTINUE + logs. Ingest worker channels removed; reaper unchanged.

---

## Medium

### M1. Commit OK + WAL delete fail → duplicate `raw_reports`

- [ ] Open

**Class:** correctness / false mismatches  
**Evidence:** `internal/storage/wal.go` (`processBatch` — `flushReportsAndEvaluate` then `deleteKeys`); `migrations/0001_init.sql` (`raw_reports` identity PK only)

**Bug:** After Postgres commit succeeds, if `deleteKeys` fails (or crash in that window), completion is not OK → retry re-INSERTs the same reports. No unique ingest key → duplicate rows. Diff treats extras as count/signature noise.

**Impact:** Rare window, but verdict-corrupting (`MISMATCH_COUNT`, noisy baseline) rather than silent drop.

**Agreed fix:**

1. Add ingest identity: **WAL seq** or explicit `ingest_id`, scoped with `replay_execution_id`.
2. Unique constraint + `ON CONFLICT DO NOTHING` on insert.
3. Do **not** unique on `(trace, role, signature, payload)` — that collapses legitimate double ops and breaks N+1 detection.
4. Retries must still re-load history and re-diff.

---

### M2. Projection soft success — SoT vs UI diverge

- [ ] Open

**Class:** correctness / UI consistency  
**Evidence:** `internal/storage/postgres_traces.go` (`SaveDiffVerdict` upsert then separate `projectTrace` with Warn + `return nil`); flush path `flushReportsAndEvaluate` / `saveDiffVerdictUnderLock`

**Bug:** `verdicts` (source of truth) can commit while `traces` / `diff_reports` / `pg_notify` fail. Tusk reads the projection; The System can look empty or stale while engine truth is updated. Projection tables are required for today’s Tusk ShadowDiff UI.

**Impact:** UI lies until a later successful project for that trace.

**Agreed fix:**

1. **Kill soft success** — projection errors fail the write.
2. One transaction: lock → reports (if any) → upsert verdict → project + notify → commit.
3. Route all verdict writes through the locked same-tx path; remove Warn-and-succeed on `SaveDiffVerdict`.
4. Keep projection tables for Tusk (do not drop the read model).

---

## Low

### L1. Ext_proc silently drops invalid JSON-looking bodies

- [ ] Open

**Class:** debuggability  
**Evidence:** `internal/envoyextproc/server.go` (`ingestResponseBody`); `internal/payload/codec.go` (`JSONCodec.Normalize` — `invalid JSON` when CT/body looks like JSON)

**Bug:** Non-JSON bodies use `RawCodec` and are kept. Bodies treated as JSON that fail `json.Valid` return an error from `FromHTTPIngress`; ext_proc ignores the error and still CONTINUE — no log, no report.

**Impact:** Hard to diagnose missing ingress roles / waiting traces when apps send broken JSON or mismatched Content-Type.

**Agreed fix:** Log convert/normalize failures (trace id, role, err). Do **not** fail the ext_proc stream.

---

### L2. Hardcoded Mongo metadata strip list

- [ ] Open

**Class:** correctness (small)  
**Evidence:** `internal/v2/diff/mongo_compare.go` (`mongoMetadataFields` = `_id`, `lsid`, `comment`, `$db`)

**Bug:** Per-connection Mongo fields outside this list produce false `MISMATCH_PAYLOAD` across roles for semantically identical ops.

**Impact:** Low until drivers/session shapes add new varying keys; then noisy regressions.

**Agreed fix (small):** Extend the strip list when observed; optionally document the list in [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md). Not blocking.

---

## Deferred

### D1. WAL overflow drop-head at 1 GiB

- [ ] Deferred

**Evidence:** `internal/storage/wal.go` (`maybeDropHead`, `walMaxFileBytes`)

Under sustained Postgres outage, oldest pending WAL entries are dropped. Accepted risk for now — do not prioritize.

---

### D2. Unauthenticated seed endpoint

- [ ] Deferred

**Evidence:** `internal/api/http.go` (`handleSeedReports`)

`POST /api/v1/debug/seed-reports` trusts shadow-namespace network isolation. OK for now.

---

## Citations

* [/data-plane/beru-analysis.md](/data-plane/beru-analysis.md) — verdict pipeline
* [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md) — WAL, SoT, projection, NOTIFY
* [/control-plane/tusk-bff.md](/control-plane/tusk-bff.md) — consumers of `traces` / `diff_reports` / `verdict_events`
* `pipeline/beru/internal/v2/diff/diff.go` — baseline vs candidate compare
* `pipeline/beru/internal/storage/wal.go` — accept vs flush durability
* `pipeline/monarch/internal/controller/shadowtest_envoy.go` — ingress `failure_mode_allow: true` (context only; not a Beru code fix)
