---
type: Architecture Specification
title: Beru Trace Analysis Engine
description: Single-trace correctness pipeline for Beru v2 — completeness timeout, baseline void guard, and compound candidate diffing with structured verdict details.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/v2
tags: [data-plane, beru, diff, analysis, verdict, baseline]
timestamp: 2026-07-24T07:00:00Z
---

# Beru Trace Analysis Engine

Beru correlates reports from `control-a`, `control-b`, and `candidate` into one verdict per `trace_id`. Every ingest re-evaluates the full timeline (`EvaluateTraceHistory`). Incomplete traces past `BERU_TRACE_TIMEOUT` (default 10s) are finalized by a WAL-safe background reaper.

## Verdict statuses

| Status | Meaning |
| --- | --- |
| `MATCH` | Baseline valid (`control-a` structurally equals `control-b`); candidate matches baseline |
| `MISMATCH` | Candidate diverges; `summary_details` JSON carries compound step flags |
| `VOIDED_BASELINE_DIVERGENCE` | Controls failed structural alignment; candidate is not evaluated |
| `WAITING_FOR_ROLES` | One or more roles missing after the timeout window (provisional — late reports overwrite) |

While roles are incomplete and age is still within the timeout, no verdict row is written.

## Evaluation sequence

```
1. Completeness → nil | WAITING_FOR_ROLES
2. Baseline guard (A vs B) → VOIDED_BASELINE_DIVERGENCE
3. Compound candidate vs A → MATCH | MISMATCH (+ flags)
```

### Baseline checks (HTTP ingress + egress)

1. HTTP ingress status codes: `control-a.StatusCode == control-b.StatusCode` (both `400` is a valid baseline).
2. Per-protocol egress operation counts must match.
3. Ordered egress signature sequences must match.

Status comparison runs **only** for HTTP ingress. Empty `status_code` on MongoDB/AMQP reports is ignored.

### Compound candidate diffs

Signature-bucket pairing accumulates **all** findings without short-circuit:

* `MISMATCH_PAYLOAD` — residual value diffs after natural noise (`Diff(A,B)`) and user `noise_filters`. Each JSON leaf emits a step with `noise_path` (dashboard **Ignore path**). Non-JSON body mismatches have no `noise_path`.
* `MISMATCH_COUNT` — `UNEXPECTED_EXTRA_EGRESS` / `MISSING_EGRESS` (no Ignore button — counts are not field filters)
* `MISMATCH_SIGNATURE` — candidate-only operation signature (no Ignore button)

`summary_details` stores JSON `VerdictDetails` (`flags`, `steps`, optional `baseline` / `missing_roles`).

## Reaper

`TraceRouter` sweeps stale incomplete traces on a short interval. Each list/load/save uses its own short context (no long-lived transaction). `WAITING_FOR_ROLES` is never terminal: any subsequent report re-evaluates and overwrites the verdict.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `BERU_TRACE_TIMEOUT` | `10s` | Age from earliest `captured_at` before incomplete traces become `WAITING_FOR_ROLES` |

## UI seed (bats / debug)

`POST /api/v1/debug/seed-reports` accepts a `reports` array of RawReport-shaped JSON (`trace_id`, `shadow_role`, `protocol`, `direction`, `signature`, `status_code`, `payload`, optional `captured_at`) and routes each into the TraceRouter — same evaluation path as live ingest.

Bats suite: `testing/bats/integration/beru/verdict_ui.bats` (mirrors unit-test histories). Leave the stack up with `BATS_KEEP=1` and port-forward `svc/beru-local:8080` to inspect the dashboard.

## Citations

* [/data-plane/index.md](/data-plane/index.md) — Data-plane document map
* [pipeline/beru/internal/v2/diff/diff.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/v2/diff/diff.go) — Evaluation implementation
* [pipeline/beru/internal/v2/engine/router.go](https://github.com/shadow-diff/monarch/tree/main/pipeline/beru/internal/v2/engine/router.go) — TraceRouter + reaper
