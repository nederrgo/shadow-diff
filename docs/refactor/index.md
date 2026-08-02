---
type: Directory Index
title: Refactor Hub
description: In-progress architectural pivots and target blueprints not yet folded into the active specs.
resource: https://github.com/shadow-diff/monarch/tree/main/docs/refactor
tags: [index, refactor, adr]
timestamp: 2026-07-28T14:00:00Z
---

# Refactor

Working proposals and target blueprints for major architectural shifts. Documents here describe accepted direction; active specs under `control-plane/`, `data-plane/`, and `architecture/` still reflect the current shipped behavior until each phase lands.

## Document Map

* [/refactor/async-record-replay.md](/refactor/async-record-replay.md) — Accepted ADR: S3-backed async Record & Replay (BYOB; Phase 4 complete — mode GC, replay trigger, prefix retention finalizer).
* [/refactor/ARCHITACTURE_SHIFT.md](/refactor/ARCHITACTURE_SHIFT.md) — Telemetry-dependent strategy: abandon absolute zero-touch; require W3C `traceparent` propagation.
* [/refactor/BERU_STATE_MACHINE_TARGET.md](/refactor/BERU_STATE_MACHINE_TARGET.md) — Target blueprint for Beru's row-level upsert state machine and signature-based pairing.
