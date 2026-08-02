---
type: Architectural Decision Record
title: In-Kernel Trace Sampling for Kaisel
description: Kaisel decides W3C traceparent sampling inside the eBPF socket filter; user space stays authoritative, the gate fails open, and the gate gains a 5.17 kernel floor with an ungated fallback below it.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel/internal/capture/bpf
tags: [data-plane, kaisel, ebpf, sampling, adr, performance]
timestamp: 2026-07-27T00:00:00Z
---

# ADR: In-Kernel Trace Sampling for Kaisel

## Context

Kaisel's eBPF socket filter matched on address, protocol and port, then copied every matched frame to user space, where `pkg/sample` applied the `samplePercentage` gate after TCP reassembly and HTTP parsing.

At a 10% sample and 100k+ RPS, nine tenths of matched production traffic was copied through the perf ring, reassembled by gopacket and HTTP-parsed purely to be discarded — on production nodes, where the manifesto's "Do No Harm" pillar makes CPU cost a first-order constraint.

The filter did not do this by oversight. It could not: the sampling decision needs the traceparent, which lives in the payload, and reading payload in the kernel was assumed impractical.

## Decision

The socket filter now decides sampling itself, for matched addresses whose rule sets `samplePercentage` below 100.

| Aspect | Choice |
| --- | --- |
| **Scope** | Only HTTP request heads are gated. Continuation segments and response halves follow their head via an LRU keyed on the canonical 5-tuple |
| **Bucketing** | The exact `pkg/sample` rule ported to C: FNV-1a-64 over the 16 decoded trace-id bytes, keep iff `(hash & 0xff) × 100 < N × 256` |
| **Authority** | User space still re-gates every report. The kernel is a pre-filter, not a replacement |
| **Undecidable cases** | Fail open — pass to user space |
| **Scan window** | 768 payload bytes, read as a head-aligned load plus a tail-aligned load that covers the gap between the payload length and the tier below it |
| **Loop construct** | `bpf_loop`, which fixes the gate's kernel floor at 5.17; below it an ungated build loads instead |
| **Percentage transport** | The `target_ips` map value, which was a presence flag, is now the per-address `samplePercentage` |

### The invariant that makes it safe

The gate is **over-permissive or exactly equal, never stricter** than user space. It drops only on a trace id it successfully parsed and bucketed out; every parse failure passes up. A kernel-side drop can therefore waste ring bandwidth but cannot lose traffic user space would have kept.

`TestGateAgreesWithPkgSample` walks 256 trace ids through the real BPF program and fails on the first disagreement with `pkg/sample`. That test is the contract.

## Consequences

### Gained

* The perf ring, TCP reassembly and HTTP parsing now see roughly the sampled share of matched traffic instead of all of it.
* Sampling ratios hold on keep-alive connections: a request head re-decides its connection rather than inheriting its predecessor's verdict.
* A SYN invalidates its 5-tuple, so recycled ephemeral ports behind SNAT cannot inherit a stale admission.

### Paid

* **The gate needs kernel 5.17.** An older kernel rejects that build, so Kaisel loads an ungated build of the same source and `pkg/sample` does all the sampling; diff results are identical, at the cost of more traffic across the perf ring. Below 5.2 neither build loads and the daemon refuses to start. See [/data-plane/kernel-compatibility.md](/data-plane/kernel-compatibility.md).
* **Bounded over-sampling.** Headers split across TCP segments, and traceparents past 768 bytes, fail open and cross ungated. Safe, but they spend ring bandwidth.
* **A lost egress mock is now possible.** On a pipelined connection, a second request that samples out can flip the LRU while the first response is still arriving, dropping its tail.
* **Two sources of truth for one rule.** The FNV-1a bucketing exists in C and in Go, and they must not drift. Mitigated by the parity test, not by construction.

### Rejected alternatives

* **An in-line loop over a narrower window.** Measured on this program at kernel 6.18, an ordinary bounded loop verifies at a 32-byte window and exceeds the 1M processed-instruction ceiling at 40 — the same ceiling for a byte-wise scan and for a 4-byte stride scan, because the binding cost is the gate downstream of the scan rather than the loop body. A 32-byte window reaches about as far as the request line; traceparent never lives there. There is no in-line loop that pays for itself here, which is what forces `bpf_loop` and its 5.17 floor.
* **Passing on fail-open without admitting the connection.** Would make the second segment of a split header an LRU miss and drop the half carrying the traceparent — trading a benign over-sample for a corrupted capture.
* **Clearing the scan buffer per packet.** 768 bytes of memset on every matched frame at 100k RPS costs more than the gate saves. Stale bytes are made unreachable by bounding every read to the tier actually loaded instead.

# Citations

- [W3C Trace Context: `traceparent` header](https://www.w3.org/TR/trace-context/#traceparent-header)
- [`bpf_loop` helper, kernel 5.17](https://docs.kernel.org/bpf/helpers.html)
- [FNV-1a hash specification](http://www.isthe.com/chongo/tech/comp/fnv/index.html)
- [BPF verifier instruction-complexity limits](https://docs.kernel.org/bpf/verifier.html)
