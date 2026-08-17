---

## type: Architecture Specification
title: Kaisel eBPF Capture Daemon
description: Self-hosted eBPF ingress and egress capture — AF_PACKET socket filter, kernel-side address/protocol/port filtering and in-kernel W3C traceparent sampling, chunked perf transport for GSO super-packets, user-space TCP reassembly, and request/response pairing for egress mocks.
resource: [https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel](https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel)
tags: [data-plane, kaisel, ebpf, capture, networking, gso, egress, sampling]
timestamp: 2026-08-10T17:00:00Z

# Kaisel eBPF Capture Daemon

Kaisel captures HTTP traffic to and from a targeted workload directly off the wire using eBPF, with no third-party control plane, no agent registration, and no outbound telemetry. It is the self-hosted capture path for Shadow-Diff's L1 layer.

One kernel filter serves both directions, because it already matches a frame whose **source or** destination is a target pod. Which direction a capture belongs to is decided in user space by which address matched:


| Direction | Match                       | Parsed                   | Sent to                                      |
| --------- | --------------------------- | ------------------------ | -------------------------------------------- |
| Ingress   | destination is a target pod | request only             | `spec.igrisBaseURL` → igris-http             |
| Egress    | source is a target pod      | request **and** response | `spec.egressBaseURL` → the ShadowTest's Shop |


A connection between two target pods produces both, correctly: it is a real ingress for the receiver and a real egress for the caller.

## Status

Kaisel is a daemon driven by `**KaiselRule` CRs** emitted by Monarch's `ShadowTest` reconciler. At startup it loads the BPF program and optionally seeds maps from `-target`/`-port` flags. A controller-runtime goroutine watches `KaiselRule` objects and delivers incremental `MapUpdate` values into the capture loop; no daemon restart is needed when pod IPs change.


| Capability                                                  | State                                                                                                                                                                                          |
| ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Packet capture, filtering, reassembly, HTTP request parsing | Implemented                                                                                                                                                                                    |
| `KaiselRule` CRD reconciliation (live IP sync, no restart)  | Implemented                                                                                                                                                                                    |
| Export to igris                                             | Implemented — userspace POST to per-ShadowTest `spec.igrisBaseURL`, routed by destination IP; see [/data-plane/siphon-audit.md](/data-plane/siphon-audit.md)                                   |
| HTTP response parsing and request/response correlation      | Implemented — per-connection FIFO pairing; see [Egress capture](#egress-capture)                                                                                                               |
| Egress capture (request + response → Shop mock)             | Implemented — `spec.egressBaseURL` on `KaiselRule`, POSTed to the shadow namespace's Shop                                                                                                      |
| Sampling                                                    | Implemented — admit (drop untraced) + shared `github.com/shadow-diff/sample` `SampledIn` (FNV-1a-64 over full 16-byte trace id) in Kaisel before POST; `spec.samplePercentage` on `KaiselRule` |


## Design premise

**Dumb kernel, smart user space.** The eBPF program does only what must happen in the kernel: reject traffic cheaply and move matched bytes across the boundary. Every decision that needs parsing — TCP reassembly, HTTP framing, trace correlation — lives in Go, where it can be tested without root, iterated without a verifier, and fixed without a node restart.

## Pipeline

```
AF_PACKET raw socket (bound to one interface)
  └─ SEC("socket") BPF filter ── IPv4 → TCP → target IP → target port
       └─ (optional) trace gate ── then emit whole skb (chunked if needed)
       └─ BPF_MAP_TYPE_PERF_EVENT_ARRAY  (1 MB per CPU)
            └─ reassembler   chunks → whole packets, per CPU
                 └─ decode.Packet   Ethernet or raw-L3, autodetected
                      └─ tcpassembly   TCP streams
                           └─ http.ReadRequest   →  OnRequest
                                └─ dst IP → KaiselRule route
                                     └─ admit (traceparent + SampledIn)
                                          └─ HTTP POST → igris-http
```

### AF_PACKET and eBPF roles

Kaisel uses **both**. They are not alternatives.


| Piece                                              | Role                                                                                                                                                                                                                                             |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **AF_PACKET** (Go opens the socket, binds ifindex) | Delivery hook. The kernel taps frames for that bind (ifindex `0` = every interface in the netns) and hands the filter a **clone** of each frame — typically `skb_clone`: new `sk_buff` metadata, payload pages usually shared with the live path |
| **eBPF socket filter** (`capture.c`)               | Program attached to that socket. Flow filters and the trace gate run here. IP/port matching is an **eBPF map** feature (`target_ips` / `target_ports`), not something AF_PACKET provides natively                                                |
| **Perf event array**                               | How matched bytes reach Go. The program **copies packet bytes** into the ring (`bpf_perf_event_output` / chunked `bpf_skb_load_bytes`). It does not store the `sk_buff` itself; user space cannot keep a pointer into kernel skb memory          |


`capture()` **returns 0 on every path**. The AF_PACKET socket receive queue is never the data path — queuing a frame there would waste memory for a buffer Go never drains. Live traffic is unaffected either way: the filter only sees the tap clone.

**Clone vs copy.** Header checks during filtering read a few bytes from the cloned skb (`bpf_skb_load_bytes`). Only packets that pass the filters (and the gate, when on) pay a **full byte copy** into the perf ring — including GSO super-packets, which are decided as one skb and then split for transport (see [Chunked transport](#chunked-transport)).

Dropping eBPF and sampling only in Go is theoretically possible (the ungated build already samples in user space) but would push all interface traffic that clears the bind across the boundary before IP/port reject — more CPU, larger rings, or more drops for the same useful sample.

### Export routing

Monarch writes `spec.igrisBaseURL` and `spec.samplePercentage` on each `KaiselRule`. Kaisel rebuilds an in-memory `dstIP → route` table from all rules. Captured request direction uses the destination address as the join key to the correct shadow-namespace igris Service. IP conflicts keep the lexicographically first `namespace/name` and warn. Missing/invalid `traceparent` is dropped in Kaisel before POST; admitted traces use `github.com/shadow-diff/sample` (`V = FNV-1a-64(decoded trace id) & 0xFF`, keep iff `(V*100)<(N*256)`). igris-http also rejects untraced requests (tracing is required; no mint).

The igris forward carries the captured request **headers** (including custom
headers such as `X-Egress-Scenario`), minus hop-by-hop framing (`Connection`,
`Transfer-Encoding`, `Host`, `Content-Length`, …). `Host` and `traceparent` are
still set explicitly from the admit path. Igris may further redact secrets
(`Authorization`, `Cookie`) before multicasting to the shadow roles.

## Kernel filter chain

Evaluated in order; the first failure returns immediately. Every check reads header bytes only — no payload is parsed in the kernel.


| Check                                  | Offset               | Rejects                                          |
| -------------------------------------- | -------------------- | ------------------------------------------------ |
| IP version nibble `== 4`               | `l2_off`             | IPv6, ARP, non-IP framing                        |
| `20 ≤ IHL ≤ 60`                        | `l2_off` (same byte) | Malformed headers                                |
| Protocol `== 6`                        | `l2_off + 9`         | UDP, ICMP, SCTP                                  |
| Source **or** dest in `target_ips`     | `l2_off + 12`        | Untargeted workloads                             |
| Not fragmented — MF clear and offset 0 | `l2_off + 6`         | Fragments, which are counted as they are dropped |
| Source **or** dest in `target_ports`   | `l2_off + IHL`       | Health checks, metrics scrapes, sidecar chatter  |


| Data offset `20 ≤ doff ≤ 60`, payload offset within the packet | `l2_off + IHL + 12` | Malformed segments, and `plen` underflow |
| Payload-free and no SYN/FIN/RST | `l2_off + IHL + 13` | Pure ACKs |

The fragment check sits after the address match so its counter reflects target traffic rather than every fragment on the wire, and before the port read because a later fragment carries no TCP header to read ports from.

Matching on either direction is deliberate: one rule captures both the request and its response without a second filter.

FIN and RST pass despite carrying no payload: `decode.runResponses` frames a `Connection: close` body by reading to EOF, and the FIN is that EOF.

Traffic that clears this chain then meets the trace gate below, which is the only stage that reads payload.

## Trace gate

When the matched address carries a `samplePercentage` below 100, the kernel decides sampling itself rather than paying to copy traffic that user space would discard. At 100k+ RPS with a 10% sample this is the difference between reassembling every request and reassembling one in ten. At 100% (or unset) the gate is skipped entirely.

The gate runs on the **whole skb** after the flow filters and **before** any perf emit or GSO chunking. Sampling is never per-chunk.


| TCP payload                                  | Kernel action                                                                                                                                           |
| -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Starts with an HTTP method token and a space | Treat as a **request head**: scan up to `HEADER_WINDOW` (768) bytes of **payload** for `traceparent`, bucket the trace id, admit or drop the connection |
| Anything else, connection in `admitted`      | Pass — a continuation segment or the response half of a sampled-in request                                                                              |
| Anything else, connection not in `admitted`  | Drop — it belongs to a request already gated out                                                                                                        |


### Detecting a request head

`is_request_head` looks only at the **start** of the current segment’s TCP payload. It is not a search for the word `GET` anywhere in the 768-byte window. The window is used **after** a head is recognised, to find `traceparent`.

Recognised prefixes (each requires the trailing space): `GET` , `PUT` , `POST` , `HEAD` , `PATCH` , `DELETE` , `OPTIONS` .

A body that contains `GET` later in the same segment does not re-trigger the gate. A later TCP segment whose payload **begins** with `GET`  can — see **Method-token false positives** under [Limitations](#limitations).

Responses normally begin with `HTTP/1.x`, so they are not heads; they ride the `admitted` entry from their request.

### The fail-open invariant

The gate is **over-permissive or exactly equal, never stricter** than user space. It drops only on a trace id it successfully parsed and bucketed out. Everything it cannot conclude — no traceparent, a header past the window, a header split across TCP segments, a malformed id, a payload too short to scan — passes up and lets `pkg/sample` decide.

That is what makes a kernel-side drop safe: it can waste ring bandwidth, but it cannot lose traffic user space would have kept. `TestGateFailsOpen` pins each branch.

### Bucketing must match `pkg/sample` exactly

The kernel runs the same rule as `pipeline/pkg/sample`: FNV-1a-64 over the 16 decoded bytes of the trace id, keep iff `(hash & 0xff) × 100 < N × 256`, upper and lower case hex both accepted. A divergence in either direction is silent — the kernel dropping what user space keeps loses production traffic. `TestGateAgreesWithPkgSample` walks 256 trace ids and fails on the first disagreement.

### Per-request, per-connection

Here **connection** means a **TCP connection** (one canonical endpoint pair until close), not one HTTP request. HTTP/1.1 keep-alive reuses that socket for many requests over time; client libraries and connection pools do this routinely. Kaisel’s path assumes **HTTP/1.x** framing on the wire (not HTTP/2 multiplexing).

A request head re-decides its connection, so a keep-alive stream is sampled per request rather than inheriting whatever its first request got. The `admitted` LRU then carries that decision to the segments that have no request line of their own — continuation segments, and the response half.

The LRU is keyed on the **canonical** endpoint 4-tuple (two IPs + two ports; TCP is already required by the flow filter), endpoints ordered so both directions fold onto one entry (the same reasoning as `decode.connKey`). Protocol is not a separate map field. A SYN deletes any entry under that key: a SYN means a new connection by definition, which is what makes a recycled ephemeral port behind SNAT exact rather than merely unlikely to alias.

There is **one** `admitted` slot per connection, not per in-flight request. Normal keep-alive is fine (response 1 finishes before request 2’s head). HTTP/1.1 **pipelining** (request 2 sent before response 1 completes — rare in real clients) cannot admit each request independently: the latest request head overwrites the same entry. See [LRU transition mid-response](#limitations).

### Where `admitted` is stored

The BPF **stack** is ~512 bytes per program invocation — locals only. It does not hold the connection table and does not grow with connection count.

`admitted` is a `BPF_MAP_TYPE_LRU_HASH` in kernel memory outside the stack: key = `flow_key`, value = a one-byte admit flag. `max_entries` is fixed at load time (default **32768**, flag `-admitted-entries`). When the map is full, LRU **evicts** the coldest flow rather than failing the update; eviction costs an ungated request on that flow, not a kernel OOM from an unbounded table. Large scratch buffers (`hdr_buf`, chunk scratch) are likewise **per-CPU maps** because they exceed the stack limit.

### Reading the window

`bpf_skb_load_bytes` takes its length as `ARG_CONST_SIZE`, so the read size must be a literal — hence a ladder of tiers rather than a clamp. A tier only ever sits at or below the payload length, and a traceparent is typically the *last* header, ending two bytes before the payload does. On a plain curl request (~158 bytes, tier 128) it lands squarely in the gap between them.

So the gate reads twice: the largest tier at the start of the payload, and — only when the first read found nothing — the same tier aligned to the end of the window. The two overlap and together cover everything up to `HEADER_WINDOW`, which is what makes the window mean what it says.

### Why `bpf_loop`

The scan is a `bpf_loop` callback, not a C loop, and that is what makes a window of any useful width verifiable at all. The verifier simulates every iteration of an ordinary bounded loop and cannot prune across the back-edge because the induction variable is live; unrolling instead spills the 512-byte BPF stack.

Measured on this program at kernel 6.18, an in-line loop verifies at a **32-byte** window and exceeds the 1M processed-instruction ceiling at 40 — the same ceiling for a byte-wise scan and for a 4-byte stride scan, because the binding cost is the gate *downstream* of the scan rather than the loop body. 32 bytes reaches about as far as the request line, and a traceparent never lives there. No loop-body rewrite recovers it.

`bpf_loop` verifies the callback exactly once however many times it runs, so cost stops scaling with the window. It also fixes the gate's floor at **5.17**. Below that Kaisel loads an ungated build of the same source and samples in user space instead — see [/data-plane/kernel-compatibility.md](/data-plane/kernel-compatibility.md).

The scan is anchored on the LF ending the previous header line: a field name only ever begins at a line boundary, so the 12-byte compare runs at the ~15 line starts a request head contains rather than at every offset. The request line occupies the first line, so requiring a preceding LF misses nothing.

### Map key convention

Both maps key on **host order** — the plain numeric value. `10.99.0.2` is `0x0A630002`; port 8080 is `8080`.

`target_ips` stores that address's `samplePercentage` as its value, not a presence flag: `samplePercentage` is per-ShadowTest, so it cannot be a load-time constant. `0` and `≥ 100` both mean "no gate", matching `sample.SampledIn`'s short-circuit. A packet matching two rules at once — a call between two target pods is ingress for one and egress for the other — takes the **max**, the permissive choice.

The kernel composes both byte-wise from the header rather than loading straight into a `__u32`:

```c
saddr = ((__u32)a[0] << 24) | ((__u32)a[1] << 16) | ((__u32)a[2] << 8) | a[3];
```

A direct load would reinterpret the wire's big-endian bytes in native order, making the key depend on the node's endianness and forcing Go to mirror that with `binary.NativeEndian` — correct on x86, silently matching nothing on a big-endian node. Composing in the kernel makes one rule work everywhere and lets Go seed with `binary.BigEndian` and a plain `uint16`.

### Load-time constants

Two `.rodata` values are set from user space before the program reaches the verifier, which then folds them to constants:


| Constant         | Purpose                                                                                                         |
| ---------------- | --------------------------------------------------------------------------------------------------------------- |
| `l2_off`         | Link-layer header size: `14` Ethernet, `0` raw L3                                                               |
| `port_filter_on` | `0` captures every TCP port. BPF cannot test a map for emptiness, so "filter at all" must be a constant         |
| `lo_ifindex`     | Loopback's ifindex, excluded by identity because its framing differs from every other device on an `any` socket |


The `admitted` LRU's `max_entries` is set on the spec before load for the same reason — it must be fixed before the map is created. `-admitted-entries` sizes it on dense nodes without recompiling the object.

## Chunked transport

AF_PACKET sits **before** segmentation, so it receives GSO super-packets far larger than the MTU — 62,557 bytes measured on a 1500-byte path. Meanwhile `struct perf_event_header` sizes a sample with a `__u16`, hard-capping any single perf event at 65,535 bytes.

Flow filters and the trace gate see that **one** skb first. Only a kept packet is emitted; chunking is solely how an oversized kept skb is copied into the perf ring. A sampled-out GSO message writes **no** chunks.

Packets are therefore split in the kernel and rejoined in Go:


| Constant     | Value                                                |
| ------------ | ---------------------------------------------------- |
| `CHUNK`      | 4096 (page-aligned; also the single-event threshold) |
| `MAX_CHUNKS` | 32                                                   |
| Reach        | 128 KB                                               |


**Packets at or below `CHUNK` take an unchanged single-event fast path** — `BPF_F_CTXLEN_MASK` appends the exact packet length after the metadata, no scratch buffer, no reassembly. That is the large majority of traffic, so the common case carries no added risk.

Oversized packets emit `CHUNK`-sized events carrying `{len, orig_len, offset, more}`. `bpf_skb_load_bytes` declares its length as `ARG_CONST_SIZE` — a compile-time constant — so **the final chunk is read backwards-aligned from `total - CHUNK`** rather than as a short variable-length read. It overlaps its predecessor; because user space writes each chunk at its absolute offset, the overlap rewrites identical bytes.

```
total = 62557, CHUNK = 4096

 [0..4095] [4096..8191] … [57344..61439] [58461..62556]
                                          ^ backwards-aligned; overlap self-heals
```

### Reassembly rules

A packet is processed start to finish on one CPU, so its chunks land consecutively in that CPU's ring, in order. `perf.Record.CPU` is the only correlation state needed — no flow keys, no timeouts.


| Condition                                     | Action                                     |
| --------------------------------------------- | ------------------------------------------ |
| `offset == 0 && more == 0 && len == orig_len` | Emit directly, never buffer                |
| `offset == 0`                                 | Allocate `orig_len`, drop any open partial |
| Continuation with no matching start           | Drop; missing chunks cannot be recovered   |
| `more == 0`                                   | Emit; `Truncated = covered < orig_len`     |
| Kernel reports `LostSamples`                  | Discard all partials for that CPU          |
| `orig_len > 128 KB`                           | Reject and count; never allocate           |


The `covered` watermark serves as both the completeness check and truncation detection, so `pkt_meta` needs no separate flag.

`packetsDiscarded` is counted separately from the kernel's `LostSamples`: "records the ring dropped" and "packets we could not put back together" are different operational signals.

## Egress capture

An egress mock is useless without the response, so egress is the one path that
must parse **both halves** of a TCP connection and join them.

```
        target pod  ──request──▶  dependency
                    ◀─response──

 tcpassembly delivers each DIRECTION as its own stream, on its own goroutine
              │                                    │
   peek != "HTTP/"                         peek == "HTTP/"
   = request side                          = response side
              │                                    │
   http.ReadRequest                     streamBuffer (drains at full speed)
              │                                    │
        publish ──────────▶ connTable ────────▶ await
                        (canonical 4-tuple)        │
                                        http.ReadResponse(buf, req)
                                                   │
                                    src ∈ target_ips? → EgressRecord
                                                   │
                                        POST /v1/record_egress
```

### Why pairing is per-connection FIFO

`http.ReadResponse` takes the request because response framing depends on it: a
`HEAD` reply carries no body, `204` and `304` carry no body, and only the request
disambiguates. Keying the join on the traceparent instead would leave
`ReadResponse` guessing and mis-read those bodies. HTTP/1.1 responses return in
request order on a connection, so FIFO is both correct and trivial.

Direction is decided by peeking for the `HTTP/` prefix rather than by comparing
addresses. That keeps `internal/decode` unaware of what a target is, and stays
correct when *both* endpoints are targets.

### Which connections get paired

Parsing responses costs memory, and only egress consumes them, so pairing is
gated per connection by a router lookup: does this connection's **source** have
an `egressBaseURL`? Ingress connections answer no and their response streams are
drained and discarded without buffering.

The gate is defined on the **request direction**, which makes it subtle: the two
half-streams arrive as separate flows, and the response half runs
dependency→target. Asked with its own flow it would test the *dependency's*
address, answer no, and discard the response half — leaving the request half with
nothing to pair against and silently producing no egress records at all. `decode`
therefore reverses the response half's flow before asking.

Widening the predicate to accept either endpoint would also work, but would pair
every ingress connection too and buffer response bodies nothing reads. Reversing
keeps the gate exact.

### Why the response side needs a buffer

`tcpreader.ReaderStream` is synchronous — the assembler's `Reassembled` call is a
blocking send that only completes once the stream's goroutine reads it. A
response parser that stopped reading to wait for its request would therefore stop
the assembler from delivering **that very request**: response waits for request,
response blocks request. Nothing but a timeout breaks it, and whenever the
assembler releases both directions response-first, pairing fails outright.

A copier goroutine drains the reassembled stream into a bounded `streamBuffer`
at full speed while the parser reads at its own pace. Past the limit the buffer
marks itself overflowed and keeps accepting writes, so the copier still reaches
EOF and the assembler still makes progress.

### What gets recorded

Kaisel sends **flat fields**; Shop derives the mock key:

```json
POST {egressBaseURL}/v1/record_egress
{ "trace_id": "<32-hex>", "method": "GET", "host": "api.example.com:8443",
  "path": "/v1/users?active=true",
  "response": { "status": 200, "headers": {...}, "body": "..." } }
```

`replay.TraceKey` and `HostWithoutPort` run inside Shop, on **both** the seed path
and the ext_proc lookup path. Kaisel computing its own key would be a second copy
of the format, free to drift from what Envoy actually looks up.


| Field              | Rule                                                                                                                                                                                                                                                      |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `host`, `method`   | Sent **verbatim** off the wire. Shop upper-cases the method and strips the host's port on both sides, so normalizing here could only introduce a mismatch — lower-casing in particular, since the lookup side never lower-cases a mixed-case `:authority` |
| `path`             | `RequestURI`, **query string included**, matching Envoy's `:path` pseudo-header                                                                                                                                                                           |
| `trace_id`         | Required. An untraced call cannot be keyed and is dropped before the POST                                                                                                                                                                                 |
| `response.headers` | Allowlist — see below                                                                                                                                                                                                                                     |
| `response.body`    | Capped at 1 MB. Over-cap records are **dropped, never truncated**                                                                                                                                                                                         |


Shop returns `{"hash": "<key>"}`, which Kaisel logs. That hash is Shop's own
computed key, so the log line shows the key Envoy will look up rather than one
Kaisel guessed at.

### Response header allowlist


| Kept                                                       | Dropped                                                | Reason for dropping                                                                                                                                 |
| ---------------------------------------------------------- | ------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `content-type`, `cache-control`, `etag`, `location`, `x-*` | `content-length`, `transfer-encoding`                  | Envoy synthesizes framing on the ext_proc immediate-response path; a captured length that no longer matches the replayed body truncates or hangs it |
|                                                            | `connection`, `keep-alive`, `te`, `trailer`, `upgrade` | Hop-by-hop: they describe the captured connection, not the replayed one                                                                             |
|                                                            | `date`, `server`                                       | Differ on every capture; pure diff noise attributable to nothing                                                                                    |


Shop's stored `EarlyResponse.Headers` is a `map[string]string`, so a multi-value
header collapses to its first value.

### Egress needs no separate filter

The filter matches `saddr` **or** `daddr` against `target_ips`, so egress frames pass through the same chain as ingress. The deployed DaemonSet passes no `-port` flags, so `port_filter_on` loads as `0` and all TCP for target IPs crosses to user space; `spec.targetPorts` is populated by Monarch but takes effect only when the daemon runs with `-port`.

## Configuration


| Flag                | Default | Meaning                                                                                                                                                                                                                                                |
| ------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `-iface`            | `lo`    | Interface to bind the raw socket to. `any` binds ifindex 0 (every interface in the netns — see "any interface capture" below). The DaemonSet's `kaisel-config` ConfigMap sets this to `any`; the CLI default of `lo` only applies to a bare manual run |
| `-kubeconfig`       | `""`    | Path to kubeconfig; empty uses in-cluster config                                                                                                                                                                                                       |
| `-namespace`        | `""`    | Namespace to scope KaiselRule watch; empty watches all                                                                                                                                                                                                 |
| `-target`           | —       | Seed IPv4 address for BPF maps (manual/test runs; controller overrides live)                                                                                                                                                                           |
| `-port`             | all     | Seed TCP port for BPF maps (manual/test runs)                                                                                                                                                                                                          |
| `-l2-off`           | `-1`    | `-1` autodetect (defaults to Ethernet when `-iface any`, since there is no single sysfs entry to read), `0` raw L3, `14` Ethernet                                                                                                                      |
| `-percpu-buffer`    | 1 MB    | Per-CPU perf ring bytes                                                                                                                                                                                                                                |
| `-admitted-entries` | 32768   | `admitted` LRU `max_entries` (kernel map of sampled-in connections). Raise on dense nodes; `0` selects the default. Fixed at program load                                                                                                              |
| `-log-bodies`       | `false` | Include captured request body content (truncated to 4KB) in logs. Off by default — bodies are real production data and pod logs are commonly shipped off-node. The DaemonSet exposes this as `logBodies` in the `kaisel-config` ConfigMap              |


In daemon mode, targets and ports come from `KaiselRule` CRs via the controller. The `-target` and `-port` flags seed the initial BPF maps before the controller has reconciled; they are additive with whatever the controller pushes later.

Requires `CAP_BPF` (or `CAP_SYS_ADMIN` on older kernels) and `CAP_NET_RAW`. The BPF object is GPL-licensed because `bpf_perf_event_output` is a GPL-only helper.

## Deployment & Security

Kaisel runs as a DaemonSet in the `kaisel-system` namespace. The namespace carries the `privileged` Pod Security Standard because `CAP_BPF`, `CAP_NET_RAW`, and `CAP_PERFMON` are blocked by the `baseline` standard. Within that namespace the DaemonSet deliberately avoids `privileged: true`.

### Privilege model


| Control                    | Value                     | Rationale                                                                                                                                                    |
| -------------------------- | ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `privileged`               | `false`                   | Grants all 40+ capabilities; never needed                                                                                                                    |
| `capabilities.add`         | `[BPF, NET_RAW, PERFMON]` | Minimum set for eBPF load, raw socket, perf ring                                                                                                             |
| `capabilities.drop`        | `[ALL]`                   | Drop all before adding back only what is needed                                                                                                              |
| `allowPrivilegeEscalation` | `false`                   | Prevents setuid / file-capability escalation                                                                                                                 |
| `readOnlyRootFilesystem`   | `true`                    | No writable process filesystem                                                                                                                               |
| `seccompProfile`           | `Unconfined`              | RuntimeDefault blocks `bpf()` and `perf_event_open()`; a scoped custom profile is the upgrade path                                                           |
| `runAsUser`                | `0`                       | Root needed on kernels < 5.11 for `setrlimit(RLIMIT_MEMLOCK)`. On 5.11+ (memcg BPF accounting) this becomes a no-op; the UID requirement can then be dropped |
| PSA level                  | `privileged`              | Required by the capability set above                                                                                                                         |


### Kubernetes RBAC

The `kaisel` ClusterRole grants `get/list/watch` on `KaiselRules` only. No write access to any resource. No access to Secrets, ConfigMaps, or any core API objects.

### Network interface: `any` (cloud-agnostic, sees same-node pod traffic)

The DaemonSet runs with `hostNetwork: true` so the container operates in the node's network namespace. The AF_PACKET socket binds to **ifindex 0** ("any" — the `kaisel-config` ConfigMap default) rather than a single named device. This is the same mechanism `tcpdump -i any` uses: the kernel delivers a clone of every send/receive event on every interface in that network namespace to the one socket, instead of only one device's traffic.

**Why a single named interface (`eth0`) is not enough.** A node's CNI connects same-node pods through a Linux bridge and veth pairs. When the bridge *forwards* a frame between two pods on the same node, that is an internal switching decision — the bridge only clones a frame to a promiscuous listener (an `AF_PACKET` socket) when the frame is addressed to or from the bridge device itself. A frame it merely relays between two other ports never reaches a listener on `eth0`, the bridge device, or any single interface — confirmed empirically: `tcpdump -i eth0` shows zero packets for a pod-to-pod request that the target actually received and answered. `any` sidesteps this because the destination pod's own host-side veth **transmitting** that frame is itself a per-device event current interface listeners see, and `any` is subscribed to every device's events at once, not one device's.

**What `any` costs, and how it's mitigated.** Binding to every interface means every pod scheduled on the node — not just external clients or cross-node peers — can now present packets to kaisel's in-kernel filter. The confidentiality gate (the `target_ips`/`target_ports` match) is unchanged and still runs before anything reaches user space, so this does not add exposure *if the filter is correct*. It does widen the blast radius *if it is ever wrong*:

- **Stale target IPs.** Kubernetes recycles pod IPs quickly. A `KaiselRule` still holding a deleted pod's IP can start matching a different pod that inherits it before Monarch's reconcile catches up. Under `eth0`-only this could only leak external/cross-node flows; under `any` it can leak **any other pod's traffic on the node**, including a different tenant's.
- **Mixed link-layer framing.** The kernel filter reads the IP header at one fixed byte offset (`l2_off`), set once at load time — it cannot detect per-packet framing. Loopback is the one device on a node with a genuinely different layout (raw IP, not Ethernet), so it is excluded **by ifindex**, not by framing guesswork: `lo_ifindex` is a `.rodata` constant resolved from `net.InterfaceByName("lo")` and set before load, and the kernel program's first check is `skb->ifindex == lo_ifindex → drop`. This closes the one concrete mis-parse risk `any` introduces; it is a no-op when bound to a single named interface (0 never equals a real ifindex).
- **Wider kernel-filter attack surface.** Any bug in the eBPF verifier or JIT is now reachable by any co-located pod, not only off-node traffic.

This is an accepted trade-off, not an oversight: it buys same-node pod-to-pod capture with zero per-CNI code and no new Linux capability (`CAP_NET_RAW` already implies "bind to any interface"), at the cost of a larger exposure surface if the filter or a `KaiselRule` is ever wrong. The alternative — resolving each target pod's own host-side veth and attaching a separate capture instance per pod, so the kernel filter only ever sees traffic for pods explicitly named in a `KaiselRule` — closes all three risks above but requires per-pod network-namespace resolution (PID discovery, ifindex correlation) instead of one static bind. Revisit that design if the cluster is multi-tenant and this exposure surface is unacceptable.

To shrink back to `eth0`-only behavior (no same-node pod-to-pod capture, smaller exposure surface), set `iface: eth0` in the ConfigMap — no code change required either way.

Deploy with:

```bash
kubectl apply -k pipeline/kaisel/deploy/
```

### Monarch RBAC

Monarch splits permissions across two ClusterRoles:

| ClusterRole | Scope | Resources | Verbs | Reason |
| ----------- | ----- | --------- | ----- | ------ |
| `manager-role` | Cluster | `namespaces` | create/delete/get/list/watch | Shadow namespace lifecycle |
| `manager-role` | Cluster | `deployments`, `replicasets`, `pods` | get/list/watch | Target workload discovery for `KaiselRule` |
| `manager-role` | Cluster | `secrets`, `configmaps`, `services` | get/list/watch | Informer cache + cred copy; no cluster-wide writes |
| `manager-role` | Cluster | `rolebindings` | full | Create per-ns binding to `shadow-workload-role` |
| `manager-role` | Cluster | `clusterroles` (`resourceNames: shadow-workload-role`) | `bind` | Privilege-escalation exception so the SA can grant shadow writes it does not hold cluster-wide |
| `manager-role` | Cluster | `shadowtests`, `kaiselrules` | full + status | CRD owner |
| `shadow-workload-role` | **Per shadow ns** (via `RoleBinding`) | `configmaps`, `secrets`, `services`, `deployments` | full | Igris/Envoy/Shop/Beru/ABC stack |
| `shadow-workload-role` | Per shadow ns | `pods` | get/list/watch | Readiness / boot gates |


Monarch's own pod runs under the `restricted` Pod Security Standard: `runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, `capabilities.drop: ALL`, `seccompProfile: RuntimeDefault`. No Linux capabilities are needed.

## Design rationale

### Socket filter, not TC or XDP

`BPF_PROG_TYPE_SOCKET_FILTER` on an AF_PACKET socket receives a **clone** of each frame. Its return value cannot drop, delay, or reorder live traffic — the capture path is fail-open *by construction rather than by convention*. A TC or XDP program sits in the forwarding path, where a verifier-accepted but semantically wrong program can black-hole production traffic. For a tool whose entire purpose is observing production without touching it, structural safety beats a careful implementation.

Returning `0` from `capture()` reinforces the same split: the program never asks the kernel to queue the clone onto the AF_PACKET socket buffer for `recvfrom`; the perf ring is the only consumer. See [AF_PACKET and eBPF roles](#af_packet-and-ebpf-roles).

### Perf array, not ring buffer

`BPF_MAP_TYPE_RINGBUF` is better in every respect but requires kernel 5.8. Target nodes may run older kernels, so the transport is a perf event array. `PacketSource` exists as a two-method interface so a ringbuf implementation can be swapped in when that floor rises, without touching decode or the HTTP layer.

### Chunking, not a larger capture clamp

The `__u16` size field caps a perf sample at 65,535 bytes. A 64 KB clamp sits at the ceiling with no headroom and fails outright for BIG TCP (kernel ≥ 5.19, up to 512 KB). Chunking is not a memory optimisation — it is the only mechanism that can move packets approaching or exceeding the perf sample limit at all.

### Bodies must be complete

A truncated body is worse than a dropped one. Igris replays one captured request to all three shadow roles, so a partial body means all three receive identical malformed input, all three fail identically, the diff is clean, and **the shadow test reports green having exercised only the error path**. That is silent loss of coverage — the most expensive failure mode available, because nobody investigates a pass. Truncation is therefore always counted and logged, never inferred.

### The gate reads payload; the flow filters do not

Every stage before the trace gate reads header bytes only. The gate is the one place that touches payload, and it is entered only when a rule sets `samplePercentage` below 100 — so a deployment that does not sample pays nothing for it.

Sizing the ring follows from that. What has to fit is the sampled share of matched traffic plus whatever fails open: untraced requests, and heads whose traceparent falls outside the window. The gate decides per request rather than smoothing rate, so the ring still has to absorb bursts of that; `-percpu-buffer` raises the ceiling.

### The scratch buffer is never cleared

`hdr_buf` holds bytes from this CPU's previous packet between invocations, and clearing it would cost more than the gate saves at 100k RPS. Stale bytes are unreachable instead: every read is bounded by the tier length actually loaded, never by the buffer size. BPF runs with preemption disabled, so two invocations cannot interleave on one CPU.

### Fragments are dropped loudly rather than quietly

Dropping fragments in the kernel is not what makes the pipeline correct — gopacket already refuses to decode a transport layer from any fragment, so they never reach the assembler regardless. What the kernel filter adds is a **reason**. A flow that produces no records because its datagrams were fragmented is otherwise indistinguishable from a flow that produced no traffic, and "captured nothing, no idea why" is the failure mode this daemon exists to avoid. The counter turns it into a warning naming the cause.

### Vendored headers with zero system includes

`bpf_helpers.h` declares its own types, map constants and helper pointers rather than including kernel UAPI headers, so the build does not depend on the host's `linux-libc-dev` version. Byte-order helpers are deliberately absent — composing bytes by hand costs nothing and keeps the dependency at zero.

### Per-CPU scratch buffer

`struct chunk_buf` is 4112 bytes against a 512-byte BPF stack limit, so the scratch buffer is a `BPF_MAP_TYPE_PERCPU_ARRAY`. This is not an optimisation; the program would not load otherwise.

### CNI-agnostic framing

Framing is autodetected by reading the interface's ARPHRD type from sysfs, and `decode.Packet` additionally falls back to the other framing if the first yields no network layer. The fallback is guarded by an explicit ethertype and version-nibble check, because gopacket's IPv4 decoder will happily return a garbage-but-non-nil layer from the first 20 bytes of an Ethernet frame. Together these cover Cilium, Calico, AWS VPC CNI, loopback and bare-L3 tunnels without per-CNI configuration.

## Limitations


| Limitation                                        | Detail                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Mitigation                                                                                                                                                                                                                                                                                                                                                                                      |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **TLS is opaque**                                 | Packet-layer capture sees ciphertext; encrypted traffic yields zero records                                                                                                                                                                                                                                                                                                                                                                                                             | Expected: no misparsing occurs. Plaintext-internal meshes only                                                                                                                                                                                                                                                                                                                                  |
| **128 KB packet ceiling**                         | `MAX_CHUNKS` bounds an unrolled loop, so `CHUNK × MAX_CHUNKS` is baked into the object and not runtime-tunable. BIG TCP can exceed it                                                                                                                                                                                                                                                                                                                                                   | Truncation is counted and logged, never silent                                                                                                                                                                                                                                                                                                                                                  |
| **Ingress responses unparsed**                    | Egress pairs request with response; ingress still forwards the request only, since nothing downstream consumes an ingress response                                                                                                                                                                                                                                                                                                                                                      | Response bytes are still captured and reassembled; parsing them is a later step                                                                                                                                                                                                                                                                                                                 |
| **Egress pairing is best-effort**                 | A response whose request was never seen (missed packets) cannot be framed and is abandoned; a request whose response side never drains is dropped                                                                                                                                                                                                                                                                                                                                       | Both are counted and logged as `unpaired` — never mispaired, which would seed a mock with the wrong response                                                                                                                                                                                                                                                                                    |
| **1 MB egress response cap**                      | A larger dependency response is dropped rather than recorded                                                                                                                                                                                                                                                                                                                                                                                                                            | Deliberate: a truncated mock replayed to all three roles yields identical failures, a clean diff, and a green report that tested nothing                                                                                                                                                                                                                                                        |
| **IPv4 only**                                     | The filter reads IPv4 headers; IPv6 is rejected at the version nibble                                                                                                                                                                                                                                                                                                                                                                                                                   | —                                                                                                                                                                                                                                                                                                                                                                                               |
| **No fragment reassembly**                        | Fragmented datagrams are dropped whole. gopacket independently declines to decode a transport layer from any fragment, so the kernel filter changes visibility rather than behaviour                                                                                                                                                                                                                                                                                                    | Drops are counted in the kernel and logged, so an affected flow has a stated cause instead of vanishing. TCP negotiates MSS and sets DF, so fragmented TCP effectively does not occur in-cluster                                                                                                                                                                                                |
| **Ring drops under burst**                        | The gate decides per request rather than smoothing rate, so a sufficiently large burst still overruns the ring                                                                                                                                                                                                                                                                                                                                                                          | Drops are logged with both counters; `-percpu-buffer` raises the ceiling                                                                                                                                                                                                                                                                                                                        |
| **Trace gate needs 5.17**                         | The gate scans with `bpf_loop`; an older kernel rejects that build                                                                                                                                                                                                                                                                                                                                                                                                                      | Kaisel loads an ungated build instead and `pkg/sample` does all the sampling. Diff results are identical; more matched traffic crosses the perf ring. Below 5.2 nothing loads and the daemon refuses to start, naming the requirement — see [/data-plane/kernel-compatibility.md](/data-plane/kernel-compatibility.md)                                                                          |
| **Traceparent past 768 bytes**                    | A header pushed beyond `HEADER_WINDOW` by large Cookie or Authorization headers is not found                                                                                                                                                                                                                                                                                                                                                                                            | Fails open: the request crosses to user space ungated and `pkg/sample` decides. Costs ring bandwidth, never coverage                                                                                                                                                                                                                                                                            |
| **Headers split across TCP segments**             | A request line in one segment and its traceparent in the next: the first segment is a head with no traceparent, so it fails open and admits the connection; the second has no request line, hits the LRU, and passes ungated                                                                                                                                                                                                                                                            | Accepted. Over-sampling stays inside the fail-open invariant — user space re-gates, so nothing extra is forwarded. Passing without admitting would instead drop the segment carrying the traceparent, turning a benign over-sample into a corrupted capture                                                                                                                                     |
| **LRU transition mid-response**                   | On a pipelined HTTP/1.1 connection, request 2’s head overwrites the single per-connection `admitted` entry. If it samples out while response 1 is still arriving, late segments of response 1 are dropped. True parallel requests on one TCP socket are not a normal keep-alive pattern; pipelining is rare                                                                                                                                                                             | Harmless to production — the filter sees a clone — and gopacket discards the truncated stream. The real cost is a lost egress mock: response 1 belonged to a sampled-in request. Needs a second request to arrive mid-response *and* to sample out                                                                                                                                              |
| **Recycled ports behind SNAT**                    | Hundreds of flows share a source and destination IP, distinguished only by the client's ephemeral port                                                                                                                                                                                                                                                                                                                                                                                  | A SYN deletes any entry under its 5-tuple, and a request head re-decides its connection, so a stale entry cannot survive either event                                                                                                                                                                                                                                                           |
| **Method-token false positives**                  | `is_request_head` only checks whether **this segment’s TCP payload begins** with a method + space. A JSON/body field containing `GET` mid-segment does not match. A TCP split that places `GET` at the **start of a later segment** (body cut so the next segment opens with `GET hello`) does: the gate treats it as a new request head. With no usable `traceparent` it **fails open and writes `admitted`**, so a connection that was sampled out can start passing again mid-stream | Trailing space makes chance matches uncommon; traffic already matched target IP/(optional) port; user-space reassembly + `http.ReadRequest` see a byte stream and discard malformed HTTP rather than inventing a clean request. Cost is over-permissive ring/CPU use, not silent loss of a request user space would have kept. The next real request head re-decides from its own `traceparent` |
| `**any` widens the kernel-filter attack surface** | Binding to every interface (see "Network interface: `any`" above) means every co-located pod, not just off-node traffic, can present packets to the in-kernel filter                                                                                                                                                                                                                                                                                                                    | Confidentiality gate (target IP/port match) is unchanged; set `iface: eth0` in the ConfigMap to shrink back to a single device if this is unacceptable                                                                                                                                                                                                                                          |
| **Lost samples cost whole packets**               | After a drop, continuity is unprovable, so in-flight partials for that CPU are discarded                                                                                                                                                                                                                                                                                                                                                                                                | Deliberate: a partly-filled buffer would hand `tcpassembly` plausible-looking corrupt bytes                                                                                                                                                                                                                                                                                                     |


## Verification


| Suite                                                     | Command                 | Requires                                                                                |
| --------------------------------------------------------- | ----------------------- | --------------------------------------------------------------------------------------- |
| Unit — reassembly, framing, IP keys                       | `make test`             | Nothing                                                                                 |
| Integration — real kernel, real BPF, real traffic         | `make test-integration` | root                                                                                    |
| Codegen contract                                          | `make verify-generate`  | clang-18                                                                                |
| Trace gate — `BPF_PROG_TEST_RUN` against the real program | `make test-integration` | root, kernel ≥ 5.17                                                                     |
| Tier selection and the ungated build                      | `make test-integration` | root; the fallback is forced through a test seam, since no pre-5.17 kernel is available |
| Cluster E2E — Monarch → prod → Kaisel → igris → shadows   | `make test-bats-kaisel` | cluster + image load                                                                    |


The trace-gate tests drive synthetic frames straight into the program with `BPF_PROG_TEST_RUN`, so no network lab is needed. `capture()` returns 0 on every path — the perf ring is its only consumer — so pass and drop are asserted on the perf ring itself rather than a return value, which is also the property the daemon cares about.


| Test                                 | Pins                                                                             |
| ------------------------------------ | -------------------------------------------------------------------------------- |
| `TestGateAgreesWithPkgSample`        | 256 trace ids bucket identically in C and in `pkg/sample`                        |
| `TestGateFailsOpen`                  | No traceparent, past the window, malformed, and too-short-to-scan all pass       |
| `TestGateCoversTheTierGap`           | The tail-aligned second read finds a traceparent the head-aligned read misses    |
| `TestGateReEvaluatesEachRequestHead` | Keep-alive connections are sampled per request                                   |
| `TestGateSYNInvalidatesRecycledPort` | A SYN clears stale admission for its 5-tuple                                     |
| `TestGateResponseFollowsItsRequest`  | Responses ride their request's decision; unknown 5-tuples drop                   |
| `TestGateZeroPayloadSegments`        | Pure ACKs drop; FIN and RST pass                                                 |
| `TestSamplingEndToEnd`               | Real curls over a veth pair: only in-bucket trace ids survive the whole pipeline |


Cluster E2E (`testing/bats/e2e/kaisel-capture/kaisel_capture.bats`) waits for `ShadowTest` Ready + `kaiselPhase`, curls the prod pod with a W3C `traceparent`, then asserts Kaisel capture logs, igris `multicast complete` for that trace, and app access logs on control-a / control-b / candidate. The egress tests drive the target into calling its dependency and assert the mock
key Shop returned in its `hash` response field — Shop's own key, so a match proves
seed and lookup agree. They match the key itself rather than a `hash=` prefix,
because `slog` quotes any value containing `=`: a key carrying a query string is
logged as `hash="…?active=true"` while one without is logged bare.

**Attribution.** Kaisel is the only writer to `/v1/record_egress`, so a mock in
Shop can only have come from it. The logged `hash` is Shop's own computed key,
which additionally proves the record was keyed exactly as the ext_proc replay
path will look it up.

The prod workload is `testing/example-apps/egress-test-app`: one binary serving
both roles, `/egress/*` as the caller (propagating the inbound trace context onto
its outbound calls) and `/dep/*` as the dependency (deterministic status, size
and header responses). Using a purpose-built app rather than a static server is
what lets an egress test exercise a specific behaviour — a `503` dependency, a
large body, several calls under one trace — instead of only the happy path.

`GET /egress/run` selects that behaviour from the `X-Egress-Scenario` header so
each E2E case is one curl into the prod pod:


| `X-Egress-Scenario` | Outbound call                                                                  | Asserted Shop host / path                          |
| ------------------- | ------------------------------------------------------------------------------ | -------------------------------------------------- |
| `large-body`        | `{DEPENDENCY_BASE_URL}/dep/size/512000`                                        | short Service name; Kaisel log `body_bytes=512000` |
| `external`          | `{EXTERNAL_BASE_URL}` (default `http://httpbin.org/get`)                       | `httpbin.org` / `/get`                             |
| `in-cluster`        | `{DEPENDENCY_CLUSTER_URL}/dep/echo` (`*.svc.cluster.local`)                    | FQDN Service host / `/dep/echo`                    |
| `parallel`          | Concurrent GETs to `/dep/echo?route=a`, `/dep/echo?route=b`, `/dep/status/200` | three distinct mock keys under one trace           |


The path/query `/egress/get?path=` routes remain for the original single-call tests.

**Envoy replay + Beru HTTP egress diff.** One prod curl is enough: Kaisel
forwards the ingress request (including `X-Egress-Scenario`) to igris, seeds
Shop from the paired prod egress, and the three shadow roles dial the same FQDN
path under that `traceparent`. Shadows retry Envoy `599`/`500` until the mock
lands (same idea as the hybrid workers). Each shadow app logs
`http egress status=200` on a Shop hit. Shop then async-POSTs
`/api/v1/egress/diff` to beru-local; the E2E asserts three egress roles,
signature `http:GET:/dep/echo?…`, and Beru log
`No egress regression for Trace … (http)` via `beru_wait_http_egress_match`.

The integration suite builds a bridge and two network namespaces, loads the real BPF program, and drives real traffic:


| Test                                | Asserts                                                                                                                            |
| ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `TestCapturesInClusterSource`       | Pod-to-pod traffic is captured                                                                                                     |
| `TestCapturesExternalSource`        | Same target, off-cluster peer, still captured                                                                                      |
| `TestIgnoresUnrelatedPods`          | Zero records for an untargeted workload on the same wire                                                                           |
| `TestIgnoresNonTargetPort`          | Zero records off-target, **and** records on-target — so the negative result means something                                        |
| `TestLargeRequestBodyIntact`        | A 200 KB POST body arrives byte-complete, SHA-256 verified                                                                         |
| `TestTLSNotMisparsed`               | Encrypted traffic yields no records rather than garbage                                                                            |
| `TestFragmentsAreDroppedAndCounted` | Fragments increment the drop counter; an identical unfragmented datagram does not                                                  |
| `TestCapturesEgressTransaction`     | The target as a **client**: outbound request paired with its response, query string retained, `Content-Type` captured off the wire |
| `TestEgressResponseBodyIntact`      | A 200 KB response body arrives byte-complete, SHA-256 verified                                                                     |


Pairing correctness is unit-tested without root in `internal/decode`: `HEAD` and
`204` framing (the reason pairing exists at all), pipelined ordering, orphan
eviction, over-cap drop, direction-independent connection keys, and that the
pairing gate is evaluated on the request direction for **both** half-streams
(`TestPairingGateUsesRequestDirectionForBothHalves`) — the one property whose
absence disables egress recording entirely while every other test still passes. The
seed/lookup key contract is pinned in `pipeline/shop/internal/replay/keys_test.go`
— the one thing neither side could previously catch drifting.

`verify-generate` diffs the generated Go binding only, never the `.o`: object bytes are not stable across clang patch versions, while the binding is what encodes the Go/C contract — maps, `pkt_meta` layout, and `.rodata` variables.

## Forward path

Reliable body capture ultimately wants a **syscall-layer probe** (`sock_sendmsg` / `sock_recvmsg`), which observes the payload as the application wrote it: no GSO, no MTU, no segmentation, and no 64 KB perf ceiling to engineer around. Chunking makes the packet path correct; it does not make the packet path the right long-term home for bodies.

# Citations

- `[struct perf_event_header](https://github.com/torvalds/linux/blob/master/include/uapi/linux/perf_event.h)` — the `__u16 size` field capping a perf sample at 65,535 bytes.
- `[bpf_skb_load_bytes` helper definition](https://github.com/torvalds/linux/blob/master/include/uapi/linux/bpf.h) — length declared `ARG_CONST_SIZE`.
- `[sk_filter_func_proto](https://github.com/torvalds/linux/blob/master/net/core/filter.c)` — maps `BPF_FUNC_perf_event_output` to the skb-aware `bpf_skb_event_output_proto`, which is what makes `BPF_F_CTXLEN_MASK` work from a socket filter.
- [BPF ring buffer introduction (kernel 5.8)](https://docs.kernel.org/bpf/ringbuf.html) — the floor that keeps the transport on a perf array.
- `[cilium/ebpf](https://github.com/cilium/ebpf)` — loader, `bpf2go` codegen, and perf reader.
- `[gopacket/gopacket](https://github.com/gopacket/gopacket)` — `tcpassembly` stream reassembly and layer decoding.
- `[packet(7)](https://man7.org/linux/man-pages/man7/packet.7.html)` — `sll_ifindex == 0` matches any interface, the mechanism behind `iface: any` and `tcpdump -i any`.
- `[struct __sk_buff` field order](https://github.com/torvalds/linux/blob/master/include/uapi/linux/bpf.h) — `ifindex` is the 11th `__u32` (byte offset 40), which the vendored `bpf_helpers.h` partial struct must match for the verifier's context-access rewriting to resolve it correctly.
- `[http.ReadResponse](https://pkg.go.dev/net/http#ReadResponse)` — takes the originating request because response framing depends on it, which is why pairing is per-connection FIFO rather than traceparent-keyed.
- `[tcpreader.ReaderStream](https://pkg.go.dev/github.com/gopacket/gopacket/tcpassembly/tcpreader#ReaderStream)` — synchronous handoff from the assembler, the reason the response side drains through a buffer instead of blocking on pairing.
- [Shop mock keys](https://github.com/shadow-diff/monarch/blob/main/pipeline/shop/internal/replay/keys.go) — `TraceKey` and `HostWithoutPort`, applied by Shop on both the seed and the ext_proc lookup path.

