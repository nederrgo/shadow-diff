---
type: Architecture Specification
title: Kaisel eBPF Capture Daemon
description: Self-hosted eBPF ingress capture — AF_PACKET socket filter, kernel-side address/protocol/port filtering, chunked perf transport for GSO super-packets, and user-space TCP reassembly.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel
tags: [data-plane, kaisel, ebpf, capture, networking, gso]
timestamp: 2026-07-25T16:40:00Z
---

# Kaisel eBPF Capture Daemon

Kaisel captures HTTP traffic to a targeted workload directly off the wire using eBPF, with no third-party control plane, no agent registration, and no outbound telemetry. It is the self-hosted capture path for Shadow-Diff's L1 layer.

## Status

Kaisel is a standalone binary. It is **not yet reconciled by Monarch**, has no container image or DaemonSet, and does not export downstream. Targets come from command-line flags; parsed requests are logged. `OnRequest` is the seam where rule-driven capture and export attach.

| Capability | State |
| --- | --- |
| Packet capture, filtering, reassembly, HTTP request parsing | Implemented |
| `PixieStreamRule` reconciliation | Not started |
| Export to igris / OTLP | Not started |
| HTTP response parsing and request/response correlation | Not started |
| Sampling | Not started |

## Design premise

**Dumb kernel, smart user space.** The eBPF program does only what must happen in the kernel: reject traffic cheaply and move matched bytes across the boundary. Every decision that needs parsing — TCP reassembly, HTTP framing, trace correlation — lives in Go, where it can be tested without root, iterated without a verifier, and fixed without a node restart.

## Pipeline

```
AF_PACKET raw socket (bound to one interface)
  └─ SEC("socket") BPF filter ── IPv4 → TCP → target IP → target port
       └─ BPF_MAP_TYPE_PERF_EVENT_ARRAY  (1 MB per CPU)
            └─ reassembler   chunks → whole packets, per CPU
                 └─ decode.Packet   Ethernet or raw-L3, autodetected
                      └─ tcpassembly   TCP streams
                           └─ http.ReadRequest   →  OnRequest
```

## Kernel filter chain

Evaluated in order; the first failure returns immediately. Every check reads header bytes only — no payload is parsed in the kernel.

| Check | Offset | Rejects |
| --- | --- | --- |
| IP version nibble `== 4` | `l2_off` | IPv6, ARP, non-IP framing |
| `20 ≤ IHL ≤ 60` | `l2_off` (same byte) | Malformed headers |
| Protocol `== 6` | `l2_off + 9` | UDP, ICMP, SCTP |
| Source **or** dest in `target_ips` | `l2_off + 12` | Untargeted workloads |
| Not fragmented — MF clear and offset 0 | `l2_off + 6` | Fragments, which are counted as they are dropped |
| Source **or** dest in `target_ports` | `l2_off + IHL` | Health checks, metrics scrapes, sidecar chatter |

The fragment check sits after the address match so its counter reflects target traffic rather than every fragment on the wire, and before the port read because a later fragment carries no TCP header to read ports from.

Matching on either direction is deliberate: one rule captures both the request and its response without a second filter.

### Map key convention

Both maps key on **host order** — the plain numeric value. `10.99.0.2` is `0x0A630002`; port 8080 is `8080`.

The kernel composes both byte-wise from the header rather than loading straight into a `__u32`:

```c
saddr = ((__u32)a[0] << 24) | ((__u32)a[1] << 16) | ((__u32)a[2] << 8) | a[3];
```

A direct load would reinterpret the wire's big-endian bytes in native order, making the key depend on the node's endianness and forcing Go to mirror that with `binary.NativeEndian` — correct on x86, silently matching nothing on a big-endian node. Composing in the kernel makes one rule work everywhere and lets Go seed with `binary.BigEndian` and a plain `uint16`.

### Load-time constants

Two `.rodata` values are set from user space before the program reaches the verifier, which then folds them to constants:

| Constant | Purpose |
| --- | --- |
| `l2_off` | Link-layer header size: `14` Ethernet, `0` raw L3 |
| `port_filter_on` | `0` captures every TCP port. BPF cannot test a map for emptiness, so "filter at all" must be a constant |

## Chunked transport

AF_PACKET sits **before** segmentation, so it receives GSO super-packets far larger than the MTU — 62,557 bytes measured on a 1500-byte path. Meanwhile `struct perf_event_header` sizes a sample with a `__u16`, hard-capping any single perf event at 65,535 bytes.

Packets are therefore split in the kernel and rejoined in Go:

| Constant | Value |
| --- | --- |
| `CHUNK` | 4096 (page-aligned; also the single-event threshold) |
| `MAX_CHUNKS` | 32 |
| Reach | 128 KB |

**Packets at or below `CHUNK` take an unchanged single-event fast path** — `BPF_F_CTXLEN_MASK` appends the exact packet length after the metadata, no scratch buffer, no reassembly. That is the large majority of traffic, so the common case carries no added risk.

Oversized packets emit `CHUNK`-sized events carrying `{len, orig_len, offset, more}`. `bpf_skb_load_bytes` declares its length as `ARG_CONST_SIZE` — a compile-time constant — so **the final chunk is read backwards-aligned from `total - CHUNK`** rather than as a short variable-length read. It overlaps its predecessor; because user space writes each chunk at its absolute offset, the overlap rewrites identical bytes.

```
total = 62557, CHUNK = 4096

 [0..4095] [4096..8191] … [57344..61439] [58461..62556]
                                          ^ backwards-aligned; overlap self-heals
```

### Reassembly rules

A packet is processed start to finish on one CPU, so its chunks land consecutively in that CPU's ring, in order. `perf.Record.CPU` is the only correlation state needed — no flow keys, no timeouts.

| Condition | Action |
| --- | --- |
| `offset == 0 && more == 0 && len == orig_len` | Emit directly, never buffer |
| `offset == 0` | Allocate `orig_len`, drop any open partial |
| Continuation with no matching start | Drop; missing chunks cannot be recovered |
| `more == 0` | Emit; `Truncated = covered < orig_len` |
| Kernel reports `LostSamples` | Discard all partials for that CPU |
| `orig_len > 128 KB` | Reject and count; never allocate |

The `covered` watermark serves as both the completeness check and truncation detection, so `pkt_meta` needs no separate flag.

`packetsDiscarded` is counted separately from the kernel's `LostSamples`: "records the ring dropped" and "packets we could not put back together" are different operational signals.

## Configuration

| Flag | Default | Meaning |
| --- | --- | --- |
| `-iface` | `lo` | Interface to bind the raw socket to |
| `-target` | `127.0.0.1` | IPv4 address to capture; repeatable, comma-separated |
| `-port` | all | TCP port to capture; repeatable, comma-separated |
| `-l2-off` | `-1` | `-1` autodetect, `0` raw L3, `14` Ethernet |
| `-percpu-buffer` | 1 MB | Per-CPU perf ring bytes |

Requires `CAP_BPF` (or `CAP_SYS_ADMIN` on older kernels) and `CAP_NET_RAW`. The BPF object is GPL-licensed because `bpf_perf_event_output` is a GPL-only helper.

## Design rationale

### Socket filter, not TC or XDP

`BPF_PROG_TYPE_SOCKET_FILTER` on an AF_PACKET socket receives a **clone** of each frame. Its return value cannot drop, delay, or reorder live traffic — the capture path is fail-open *by construction rather than by convention*. A TC or XDP program sits in the forwarding path, where a verifier-accepted but semantically wrong program can black-hole production traffic. For a tool whose entire purpose is observing production without touching it, structural safety beats a careful implementation.

### Perf array, not ring buffer

`BPF_MAP_TYPE_RINGBUF` is better in every respect but requires kernel 5.8. Target nodes may run older kernels, so the transport is a perf event array. `PacketSource` exists as a two-method interface so a ringbuf implementation can be swapped in when that floor rises, without touching decode or the HTTP layer.

### Chunking, not a larger capture clamp

The `__u16` size field caps a perf sample at 65,535 bytes. A 64 KB clamp sits at the ceiling with no headroom and fails outright for BIG TCP (kernel ≥ 5.19, up to 512 KB). Chunking is not a memory optimisation — it is the only mechanism that can move packets approaching or exceeding the perf sample limit at all.

### Bodies must be complete

A truncated body is worse than a dropped one. Igris replays one captured request to all three shadow roles, so a partial body means all three receive identical malformed input, all three fail identically, the diff is clean, and **the shadow test reports green having exercised only the error path**. That is silent loss of coverage — the most expensive failure mode available, because nobody investigates a pass. Truncation is therefore always counted and logged, never inferred.

### Sampling cannot protect the ring

The sampling decision depends on the traceparent, which lives in the **payload** — HTTP headers, or MongoDB's `$comment` field — not in anything the kernel can cheaply read. Extraction happens in user space, after the copy. Two-stage filtering does not rescue this: body packets follow the header packet immediately, so a map update always loses the race.

**Consequence: 100% of matched traffic must cross into user space.** Sampling reduces what goes downstream; it does nothing for ring pressure. The ring is therefore sized for peak *unsampled* throughput (1 MB per CPU), and the kernel-side address, protocol and port filters are the only levers that reduce what arrives at all. This is why those filters are worth their complexity.

### Fragments are dropped loudly rather than quietly

Dropping fragments in the kernel is not what makes the pipeline correct — gopacket already refuses to decode a transport layer from any fragment, so they never reach the assembler regardless. What the kernel filter adds is a **reason**. A flow that produces no records because its datagrams were fragmented is otherwise indistinguishable from a flow that produced no traffic, and "captured nothing, no idea why" is the failure mode this daemon exists to avoid. The counter turns it into a warning naming the cause.

### Vendored headers with zero system includes

`bpf_helpers.h` declares its own types, map constants and helper pointers rather than including kernel UAPI headers, so the build does not depend on the host's `linux-libc-dev` version. Byte-order helpers are deliberately absent — composing bytes by hand costs nothing and keeps the dependency at zero.

### Per-CPU scratch buffer

`struct chunk_buf` is 4112 bytes against a 512-byte BPF stack limit, so the scratch buffer is a `BPF_MAP_TYPE_PERCPU_ARRAY`. This is not an optimisation; the program would not load otherwise.

### CNI-agnostic framing

Framing is autodetected by reading the interface's ARPHRD type from sysfs, and `decode.Packet` additionally falls back to the other framing if the first yields no network layer. The fallback is guarded by an explicit ethertype and version-nibble check, because gopacket's IPv4 decoder will happily return a garbage-but-non-nil layer from the first 20 bytes of an Ethernet frame. Together these cover Cilium, Calico, AWS VPC CNI, loopback and bare-L3 tunnels without per-CNI configuration.

## Limitations

| Limitation | Detail | Mitigation |
| --- | --- | --- |
| **TLS is opaque** | Packet-layer capture sees ciphertext; encrypted traffic yields zero records | Expected: no misparsing occurs. Plaintext-internal meshes only |
| **128 KB packet ceiling** | `MAX_CHUNKS` bounds an unrolled loop, so `CHUNK × MAX_CHUNKS` is baked into the object and not runtime-tunable. BIG TCP can exceed it | Truncation is counted and logged, never silent |
| **Requests only** | Response bytes are captured and reassembled but not parsed | Response parsing is a later step |
| **IPv4 only** | The filter reads IPv4 headers; IPv6 is rejected at the version nibble | — |
| **No fragment reassembly** | Fragmented datagrams are dropped whole. gopacket independently declines to decode a transport layer from any fragment, so the kernel filter changes visibility rather than behaviour | Drops are counted in the kernel and logged, so an affected flow has a stated cause instead of vanishing. TCP negotiates MSS and sets DF, so fragmented TCP effectively does not occur in-cluster |
| **Ring drops under burst** | Sampling cannot protect the ring, so a sufficiently large burst overruns it | Drops are logged with both counters; `-percpu-buffer` raises the ceiling |
| **Bridge devices see nothing** | A Linux bridge does not deliver frames it forwards between ports to AF_PACKET listeners on the bridge device | Attach to the target's host-side veth, which carries every frame in both directions |
| **Lost samples cost whole packets** | After a drop, continuity is unprovable, so in-flight partials for that CPU are discarded | Deliberate: a partly-filled buffer would hand `tcpassembly` plausible-looking corrupt bytes |

## Verification

| Suite | Command | Requires |
| --- | --- | --- |
| Unit — reassembly, framing, IP keys | `make test` | Nothing |
| Integration — real kernel, real BPF, real traffic | `make test-integration` | root |
| Codegen contract | `make verify-generate` | clang-18 |

The integration suite builds a bridge and two network namespaces, loads the real BPF program, and drives real traffic:

| Test | Asserts |
| --- | --- |
| `TestCapturesInClusterSource` | Pod-to-pod traffic is captured |
| `TestCapturesExternalSource` | Same target, off-cluster peer, still captured |
| `TestIgnoresUnrelatedPods` | Zero records for an untargeted workload on the same wire |
| `TestIgnoresNonTargetPort` | Zero records off-target, **and** records on-target — so the negative result means something |
| `TestLargeRequestBodyIntact` | A 200 KB POST body arrives byte-complete, SHA-256 verified |
| `TestTLSNotMisparsed` | Encrypted traffic yields no records rather than garbage |
| `TestFragmentsAreDroppedAndCounted` | Fragments increment the drop counter; an identical unfragmented datagram does not |

`verify-generate` diffs the generated Go binding only, never the `.o`: object bytes are not stable across clang patch versions, while the binding is what encodes the Go/C contract — maps, `pkt_meta` layout, and `.rodata` variables.

## Forward path

Reliable body capture ultimately wants a **syscall-layer probe** (`sock_sendmsg` / `sock_recvmsg`), which observes the payload as the application wrote it: no GSO, no MTU, no segmentation, and no 64 KB perf ceiling to engineer around. Chunking makes the packet path correct; it does not make the packet path the right long-term home for bodies.

# Citations

* [`struct perf_event_header`](https://github.com/torvalds/linux/blob/master/include/uapi/linux/perf_event.h) — the `__u16 size` field capping a perf sample at 65,535 bytes.
* [`bpf_skb_load_bytes` helper definition](https://github.com/torvalds/linux/blob/master/include/uapi/linux/bpf.h) — length declared `ARG_CONST_SIZE`.
* [`sk_filter_func_proto`](https://github.com/torvalds/linux/blob/master/net/core/filter.c) — maps `BPF_FUNC_perf_event_output` to the skb-aware `bpf_skb_event_output_proto`, which is what makes `BPF_F_CTXLEN_MASK` work from a socket filter.
* [BPF ring buffer introduction (kernel 5.8)](https://docs.kernel.org/bpf/ringbuf.html) — the floor that keeps the transport on a perf array.
* [`cilium/ebpf`](https://github.com/cilium/ebpf) — loader, `bpf2go` codegen, and perf reader.
* [`gopacket/gopacket`](https://github.com/gopacket/gopacket) — `tcpassembly` stream reassembly and layer decoding.
