---
type: Concept Guide
title: Kaisel Kernel Compatibility
description: Which build of the Kaisel eBPF filter a node's kernel accepts, how the tier is chosen at load time, and what each tier costs.
resource: https://github.com/shadow-diff/monarch/tree/main/pipeline/kaisel/internal/capture
tags: [data-plane, kaisel, ebpf, kernel, compatibility]
timestamp: 2026-07-28T00:00:00Z
---

# Kaisel Kernel Compatibility

Kaisel compiles one `capture.c` into two eBPF objects and loads whichever the node's kernel
accepts. Capture works on every kernel from **5.2** up; the in-kernel trace gate needs **5.17**.

## Feature floors

The gate is the only thing in the program that needs a modern kernel.

| Feature | Since |
| --- | --- |
| `bpf_map_lookup` / `update` / `delete_elem` | 3.18 |
| `bpf_perf_event_output` | 4.4 |
| `bpf_skb_load_bytes` | 4.5 |
| `BPF_MAP_TYPE_LRU_HASH` | 4.10 |
| 3 × `volatile const` → frozen read-only maps | 5.2 |
| **`bpf_loop`** — the trace-gate scanner | **5.17** |

## The tiers

| | Kernel | Kernel-side work | Sampling decided by |
| --- | --- | --- | --- |
| **Tier 1** `bpf_bpfel.o` | ≥ 5.17 | Flow filters **+ trace gate** (`bpf_loop`, 768-byte window) | Kernel pre-filters, user space re-gates |
| **Tier 2** `nogate_bpfel.o` | ≥ 5.2 | Flow filters only | `pkg/sample` in user space, alone |
| **Tier 3** | < 5.2 | — | Daemon refuses to start |

**Tier 2 is a performance tier, not a correctness tier.** User space has always been the authority
— the kernel gate can only ever be over-permissive, never stricter. A Tier 2 node produces
identical ShadowTest verdicts; it spends more CPU and perf-ring bandwidth doing so, because every
matched frame crosses the ring before `pkg/sample` drops it.

### What "flow filters" means

Everything below runs in **both** builds, in this order:

| Check | Drops |
| --- | --- |
| `skb->ifindex == lo_ifindex` | Loopback, whose framing differs from every other device |
| IP version nibble ≠ 4 | IPv6 |
| IHL outside 20–60 | Malformed IP header |
| `proto != IPPROTO_TCP` | UDP, ICMP, everything else |
| `target_ips` miss on both endpoints | Traffic no `KaiselRule` asked for |
| MF set or fragment offset ≠ 0 | Fragments, counted in `frag_drops` |
| `port_wanted` | Only when a rule declared ports |
| TCP data offset outside 20–60 | Malformed TCP header |
| `poff > total` | `u32` underflow on a truncated segment |
| `plen == 0` without SYN/FIN/RST | Pure ACKs |

Tier 2 removes only the payload scan and the state that serves it: `hdr_buf`, the `admitted` LRU,
`sample_drops`, and the SYN invalidation.

## How the tier is chosen

```
try bpf_bpfel.o     (gate)          ── verifier accepts ─► Tier 1
   │ rejected
try nogate_bpfel.o  (flow filters)  ── verifier accepts ─► Tier 2   (Warn, with reason)
   │ rejected
refuse to start, naming the 5.2 floor and the running kernel
```

**The load attempt is the test — not the kernel version string.** RHEL 9 ships 5.14 with `bpf_loop`
backported, so a version comparison would push RHCOS to Tier 2 on a kernel that runs the gate
perfectly well. Trying the objects in order is self-configuring and needs no kernel allow-list.

`features.HaveProgramHelper` is consulted **only to write the log line**, never to route. It is
conclusive in just two cases, infers availability from `EACCES`, and is a 3-instruction probe with
no maps — a kernel can pass it and still reject the real 435-instruction program. What it buys is
separating *"this kernel has no `bpf_loop`"* from *"your memlock is too low"*, which need opposite
responses.

## Managed Kubernetes

Observed defaults, which **move** — treat as a starting point, not an answer. The daemon's load
attempt is the authority.

| Distribution | Typical node kernel | Expected tier |
| --- | --- | --- |
| GKE — Container-Optimized OS (recent) | 6.1 | 1 |
| GKE — COS (older pools) | 5.15 | 2 |
| EKS — Amazon Linux 2023 | 6.1 | 1 |
| EKS — Amazon Linux 2 | 5.10 | 2 |
| AKS — Azure Linux / Mariner | 6.6 | 1 |
| AKS — Ubuntu 22.04 | 5.15 | 2 |
| OpenShift — RHCOS 9 | 5.14 **with heavy backports** | **Unknown — see below** |

RHCOS is flagged rather than predicted on purpose. RHEL 9 carries a 5.14 base version with large
portions of later BPF work backported, so `uname -r` does not settle whether `bpf_loop` is present.
Only the load attempt does, and the startup log reports the result.

## Checking your fleet

Node kernels:

```bash
kubectl get nodes -o wide      # KERNEL-VERSION column
```

The authoritative per-node answer is Kaisel's own startup log:

```
level=INFO msg="kaisel trace gate active" tier=1 mode=bpf_loop
```

```
level=WARN msg="kaisel trace gate unavailable; sampling runs in user space instead.
  Capture and diff results are unaffected: more matched traffic crosses the perf ring
  before pkg/sample gates it"
  tier=2 mode=userspace reason="kernel lacks bpf_loop, which needs >= 5.17"
  kernel=5.15.0-1071-azure
```

Below 5.2 the pod exits non-zero and crashloops with the requirement named. That is deliberate: a
node capturing nothing *silently* is the failure mode this daemon exists to avoid.

### Metrics

`kaisel_ebpf_gate_tier{mode="bpf_loop|userspace"}` reports the tier as a gauge (1 or 2).

It is **off by default**. The DaemonSet runs with `hostNetwork: true`, so a metrics listener binds
a real port on every node; opting in is a deliberate act:

```yaml
args:
- -metrics-bind-address=:9099
ports:
- name: metrics
  containerPort: 9099
```

A node running Tier 3 never appears — it has no running process to scrape.

## Operational differences

| | Tier 1 | Tier 2 |
| --- | --- | --- |
| Perf-ring volume | ~ sampled share of matched traffic | All matched traffic |
| `sample_drops` counter | Rises with the drop rate | Map does not exist; stays 0 |
| `"kernel trace gate dropped packets"` log | Periodic | Never fires |
| ShadowTest verdicts | Identical | Identical |

Sizing follows from the first row: a Tier 2 node at a 10% sample pushes roughly ten times the ring
traffic of a Tier 1 node at the same rate. Raise `-percpu-buffer` if `lost` appears in the logs.

## What is and is not verified

| | Coverage |
| --- | --- |
| Tier 1 program | 15 gate tests + `TestGateAgreesWithPkgSample` (`BPF_PROG_TEST_RUN`), plus cluster E2E on Kind via `make test-bats-kaisel` |
| Tier 2 object | `TestNoGateObjectLoads`, `TestNoGatePassesEverything`, `TestNoGateKeepsFlowFilters` — verifies, passes what Tier 1 gates, keeps every flow filter |
| Tier selection | `TestPrefersGateWhenAvailable` and `TestFallsBackWhenGateRejected`, the latter through a test seam on the candidate list |
| Tier 3 refusal | `TestRefusesWhenNoTierLoads` |

**No pre-5.17 node has run this.** The development host is 6.18 and local E2E runs on Kind, so
Tier 1 is the only tier exercised on real traffic. The fallback path is proven by forcing the gated
candidate to fail, not by meeting a kernel that rejects it. See [/infrastructure/minikube-to-kind-migration.md](/infrastructure/minikube-to-kind-migration.md) for cluster bootstrap details.

# Citations

- [`bpf_loop` helper, kernel 5.17](https://docs.kernel.org/bpf/helpers.html)
- [BPF verifier instruction-complexity limits](https://docs.kernel.org/bpf/verifier.html)
- [cilium/ebpf feature probes](https://pkg.go.dev/github.com/cilium/ebpf/features)
- [Red Hat Enterprise Linux 9 kernel versioning](https://access.redhat.com/articles/3078)
