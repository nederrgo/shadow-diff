---
type: Architectural Decision Record
title: Replay Execution Isolation
description: Decouple S3 capture session folders from Postgres/UI diff runs via Monarch-minted replay_execution_id injected only into beru-local.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/monarch/internal/controller
tags: [control-plane, monarch, beru, tusk, the-system, session, replay, postgres]
timestamp: 2026-08-03T14:00:00Z
---

# Replay Execution Isolation

## Context

`session_id` alone grouped both S3 capture folders and Postgres diff rows. Toggling `record → replay → record → replay` without pinning `spec.sessionID` reused `status.currentSessionID`, so new captures appended into the same S3 prefix and re-replays reused capture trace IDs. Beru evaluated `WHERE trace_id = $1`, so `raw_reports` accumulated and The System occurrence pager showed inflated counts.

## Decision

1. **Session remint** — On `mode=record` with empty `spec.sessionID`, Monarch mints `status.currentSessionID` (`sess-<unix>-<4hex>`) when status is empty or `status.replayState` is non-empty (replay→record). Pinned `spec.sessionID` is always honored.
2. **Replay execution id** — On entering replay with empty `replayState`, Monarch mints `status.currentReplayExecutionID` (`exec-<unix>-<4hex>`) and injects it as `REPLAY_EXECUTION_ID` on beru-local only. Cleared on record via `clearReplayState`.
3. **No Igris/header path** — The app under test is zero-touch and will not forward custom headers. Beru stamps every ingest path from process env. `POST /v1/replay/start` stays body-less.
4. **Postgres scope** — Rows are keyed/filtered by `(session_id, replay_execution_id)`. Tusk defaults omitted `replay_execution_id` to the latest execution for the session; The System exposes a Replay Run picker.

## Consequences

- Re-playing the same S3 session yields occurrence counts starting at 1 for that execution.
- Unpinned record cycles get fresh S3 prefixes; pinned sessions may still append in S3 but remain isolated in Postgres by execution id.
- Operators wipe/recreate Postgres when applying the greenfield schema change (no additive migration).

# Citations

* [/control-plane/monarch-controller.md](/control-plane/monarch-controller.md)
* [/data-plane/beru-postgres-storage.md](/data-plane/beru-postgres-storage.md)
* [/control-plane/tusk-bff.md](/control-plane/tusk-bff.md)
* [/control-plane/the-system.md](/control-plane/the-system.md)
