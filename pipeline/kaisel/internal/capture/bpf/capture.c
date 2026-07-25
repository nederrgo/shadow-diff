/* kaisel packet collector -- the "dumb" kernel half.
 *
 * Attached as BPF_PROG_TYPE_SOCKET_FILTER to an AF_PACKET raw socket. The
 * kernel hands this program a *clone* of each frame, so its return value
 * cannot drop, delay or reorder live traffic: the capture path is fail-open by
 * construction, not by convention.
 *
 * All policy lives in user space, with one exception. The sampling decision
 * depends on the traceparent, which lives in the payload, so it cannot happen
 * here -- 100% of matched traffic must cross into user space before it can be
 * sampled. That makes the cheap filters below (IPv4, TCP, port, address) the
 * only lever available for controlling how much data crosses at all.
 */
#include "bpf_helpers.h"

/* Bytes per perf event. Page-aligned, and also the threshold below which a
 * packet takes the single-event fast path.
 */
#define CHUNK 4096

/* Unrolled loop bound, so CHUNK * MAX_CHUNKS is baked into the object and is
 * NOT tunable at runtime.
 *
 * ponytail: 128KB ceiling. Standard GSO cannot exceed 64KB (the IP length
 * field is 16 bits), so this covers GSO and GRO aggregation with headroom.
 * BIG TCP (kernel >= 5.19) can produce up to 512KB; those packets are
 * truncated -- but reported as such, never silently. Upgrade path: raise
 * MAX_CHUNKS, or move body capture to a syscall-layer probe where
 * segmentation does not exist.
 */
#define MAX_CHUNKS 32

#define IPPROTO_TCP 6
#define IP_IHL_MIN 20
#define IP_IHL_MAX 60

/* IPv4 addresses to capture, in HOST order: 10.99.0.2 is 0x0A630002.
 * A packet matches on either source or destination.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1024);
	__type(key, __u32);
	__type(value, __u8);
} target_ips SEC(".maps");

/* TCP ports to capture, in HOST order, same as target_ips. Checked against both
 * source and destination so request and response directions both match.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 256);
	__type(key, __u16);
	__type(value, __u8);
} target_ports SEC(".maps");

/* Fragments of target TCP datagrams dropped, per CPU. Dropping loses a real
 * request, so it must be visible: this is the counter user space logs.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __u64);
} frag_drops SEC(".maps");

/* Per-CPU perf ring carrying pkt_meta followed by packet bytes. */
struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(__u32));
} events SEC(".maps");

struct pkt_meta {
	__u32 len;      /* bytes in THIS chunk */
	__u32 orig_len; /* full packet length on the wire */
	__u32 offset;   /* absolute offset of this chunk within the packet */
	__u32 more;     /* 1 = another chunk follows */
};

/* meta and payload must be contiguous for a single perf_event_output, and the
 * BPF stack is capped at 512 bytes -- hence a per-CPU scratch map.
 */
struct chunk_buf {
	struct pkt_meta meta;
	__u8 data[CHUNK];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, struct chunk_buf);
} scratch SEC(".maps");

/* Link-layer header size: 14 for Ethernet, 0 for raw L3 (ARPHRD_NONE tunnels).
 * Set from user space before load, so the verifier folds it to a constant.
 * Mirrors decode.Framing on the Go side.
 */
volatile const __u32 l2_off = 14;

/* 0 = capture every TCP port, 1 = consult target_ports. A .rodata const rather
 * than a map emptiness check, which BPF cannot express.
 */
volatile const __u32 port_filter_on = 0;

/* clang prunes BTF for types reachable only from locals, after which
 * `bpf2go -type pkt_meta` fails with "collect C types: not found".
 */
const struct pkt_meta *unused_pkt_meta __attribute__((unused));

static __inline int port_wanted(__u16 sport, __u16 dport)
{
	if (!port_filter_on)
		return 1;
	return bpf_map_lookup_elem(&target_ports, &sport) ||
	       bpf_map_lookup_elem(&target_ports, &dport);
}

SEC("socket")
int capture(struct __sk_buff *skb)
{
	__u8 ver_ihl, proto, a[8], f[2], p[4];
	__u64 *frags;
	__u32 saddr, daddr, ihl, total, off, zero = 0;
	__u16 sport, dport;
	struct chunk_buf *buf;
	int i, last;

	/* Version nibble, not ethertype: this test is framing-independent, so
	 * offset 14 (Ethernet) and offset 0 (raw L3) share a single path. The
	 * same byte carries IHL, which locates the TCP header below.
	 */
	if (bpf_skb_load_bytes(skb, l2_off, &ver_ihl, sizeof(ver_ihl)) < 0)
		return 0;
	if ((ver_ihl >> 4) != 4)
		return 0;

	ihl = (ver_ihl & 0x0f) * 4;
	if (ihl < IP_IHL_MIN || ihl > IP_IHL_MAX)
		return 0;

	if (bpf_skb_load_bytes(skb, l2_off + 9, &proto, sizeof(proto)) < 0)
		return 0;
	if (proto != IPPROTO_TCP)
		return 0;

	/* One 8-byte read covers both addresses, which sit adjacent in the IPv4
	 * header. Compose host order byte-wise rather than loading straight into
	 * a __u32: a direct load reinterprets the wire's big-endian bytes in
	 * native order, so the map key would differ between a little-endian and a
	 * big-endian node for the same address, and Go would have to mirror that
	 * with binary.NativeEndian. Composing here makes the key the plain numeric
	 * value of the dotted quad everywhere, so Go seeds it with BigEndian.
	 */
	if (bpf_skb_load_bytes(skb, l2_off + 12, a, sizeof(a)) < 0)
		return 0;
	saddr = ((__u32)a[0] << 24) | ((__u32)a[1] << 16) | ((__u32)a[2] << 8) | a[3];
	daddr = ((__u32)a[4] << 24) | ((__u32)a[5] << 16) | ((__u32)a[6] << 8) | a[7];

	if (!bpf_map_lookup_elem(&target_ips, &saddr) &&
	    !bpf_map_lookup_elem(&target_ips, &daddr))
		return 0;

	/* Drop every fragment of a fragmented datagram -- MF set (0x2000) or a
	 * non-zero fragment offset (0x1fff).
	 *
	 * This is for visibility, not correctness. gopacket already declines to
	 * decode a transport layer from any fragment, so fragments never reach the
	 * assembler either way -- but it declines silently, and a flow that
	 * vanishes without a trace is the failure mode this daemon exists to avoid.
	 * Counting here turns "captured nothing, no idea why" into a warning naming
	 * the cause. It also keeps a later fragment's port read from returning
	 * payload bytes that happen to match a target port.
	 *
	 * Checked after the address match so the counter reflects target traffic
	 * rather than every fragment on the wire, and before the port read because
	 * a later fragment carries no TCP header to read ports from.
	 *
	 * ponytail: no fragment reassembly. TCP negotiates MSS and sets DF, so
	 * fragmented TCP effectively does not occur on a uniform-MTU cluster
	 * network. Upgrade path: reassemble in user space keyed on (src, dst, id),
	 * which is where the L3 work already lives.
	 */
	if (bpf_skb_load_bytes(skb, l2_off + 6, f, sizeof(f)) < 0)
		return 0;
	if (((((__u16)f[0] << 8) | f[1]) & 0x3fff) != 0) {
		frags = bpf_map_lookup_elem(&frag_drops, &zero);
		if (frags)
			(*frags)++;
		return 0;
	}

	/* Ports follow the same host-order rule as the addresses above. */
	if (bpf_skb_load_bytes(skb, l2_off + ihl, p, sizeof(p)) < 0)
		return 0;
	sport = ((__u16)p[0] << 8) | p[1];
	dport = ((__u16)p[2] << 8) | p[3];
	if (!port_wanted(sport, dport))
		return 0;

	total = skb->len;

	/* Fast path: the packet fits in one event, so append it straight from
	 * the skb with no scratch buffer. The high 32 bits of flags
	 * (BPF_F_CTXLEN_MASK) tell the kernel how many packet bytes to copy
	 * after meta. Works because sk_filter_func_proto maps
	 * BPF_FUNC_perf_event_output to the skb-aware bpf_skb_event_output_proto.
	 */
	if (total <= CHUNK) {
		struct pkt_meta meta = { total, total, 0, 0 };

		bpf_perf_event_output(skb, &events,
				      BPF_F_CURRENT_CPU | ((__u64)total << 32),
				      &meta, sizeof(meta));
		return 0;
	}

	buf = bpf_map_lookup_elem(&scratch, &zero);
	if (!buf)
		return 0;

	/* bpf_skb_load_bytes takes its length as ARG_CONST_SIZE -- a
	 * compile-time constant. The offset may vary, so the final chunk is
	 * read backwards-aligned from total - CHUNK: always a full CHUNK read,
	 * never past the packet end, no variable length anywhere. It overlaps
	 * its predecessor, and since user space writes each chunk at its
	 * absolute offset the overlap simply rewrites identical bytes.
	 */
#pragma unroll
	for (i = 0; i < MAX_CHUNKS; i++) {
		off = (__u32)i * CHUNK;
		if (off >= total)
			break;

		/* Forcing last on the final iteration matters: without it an
		 * oversized packet emits chunks that never terminate and user
		 * space abandons the partial silently -- the exact failure this
		 * design exists to remove. Terminated here, the covered
		 * watermark reports it as truncation instead.
		 */
		last = (off + CHUNK >= total) || (i == MAX_CHUNKS - 1);
		if (last)
			off = total - CHUNK;

		if (bpf_skb_load_bytes(skb, off, buf->data, CHUNK) < 0)
			break;

		buf->meta.len = CHUNK;
		buf->meta.orig_len = total;
		buf->meta.offset = off;
		buf->meta.more = last ? 0 : 1;

		bpf_perf_event_output(skb, &events, BPF_F_CURRENT_CPU, buf,
				      sizeof(*buf));
		if (last)
			break;
	}

	/* ponytail: return 0 rather than skb->len. The perf ring is the only
	 * consumer, so letting the kernel also queue this frame onto an
	 * AF_PACKET receive buffer we never drain is pure waste. Live traffic is
	 * unaffected either way -- this is a clone. Upgrade path: return skb->len
	 * if a recvfrom() fallback path is ever added alongside the perf reader.
	 */
	return 0;
}

/* bpf_perf_event_output is GPL-only. */
char _license[] SEC("license") = "GPL";
