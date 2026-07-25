---
type: Architectural Decision Record
title: Siphon Audit — Kaisel to Igris Cutover
description: Historical ADR for replacing Siphon with Kaisel ingress capture. Siphon has been removed from the codebase.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel
tags: [data-plane, siphon, kaisel, igris, sampling, capture, adr]
timestamp: 2026-07-25T18:40:00Z
---

# Siphon Audit — Kaisel → Igris Cutover

Historical ADR for the Pixie+Siphon → Kaisel cutover. **Siphon has been removed** (`pipeline/siphon/` deleted). HTTP ingress is Kaisel → igris-http.

## Decision (final)

| Concern | Owner |
| --- | --- |
| Packet capture / HTTP parse | Kaisel eBPF + userspace |
| Admit (drop untraced) + `SampledIn` | Kaisel before POST |
| Forward to igris | Kaisel `HTTPRecord` POST |
| Require valid `traceparent` (no mint) | igris-http `ResolveContext` |
| Fan-out ×3 | igris-http |

```
Live L1:  Kaisel parse → admit + SampledIn → HTTP POST → igris-http
```

## CR knobs (current)

| Field | Consumer |
| --- | --- |
| `spec.samplePercentage` | KaiselRule + PixieStreamRule + Recorder |
| `status.kaiselPhase` | Ready / Degraded from `reconcileKaiselCapture` |

Dropped with SiphonSpec: `enabled`, `image`, `excludePaths`, `maxPayloadSize` (Pixie max payload uses Monarch default).

## Sampling rule

```
V = int(traceID[0:2], 16)
keep iff (V * 100) < (N * 256)
```

Same math in Kaisel, Recorder, igris-rabbitmq, and Pixie egress PxL.

# Citations

* [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md) — live Kaisel capture + export.
* [`pipeline/kaisel/internal/sample/sample.go`](https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel/internal/sample/sample.go) — `SampledIn`.
* [`pipeline/igrises/igris-http/internal/trace/resolve.go`](https://github.com/shadow-diff/monarch/tree/main/pipeline/igrises/igris-http/internal/trace/resolve.go) — reject missing/invalid `traceparent`.
