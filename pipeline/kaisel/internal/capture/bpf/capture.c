/* kaisel packet collector -- the kernel half.
 *
 * Attached as BPF_PROG_TYPE_SOCKET_FILTER to an AF_PACKET raw socket. The
 * kernel hands this program a *clone* of each frame, so its return value
 * cannot drop, delay or reorder live traffic: the capture path is fail-open by
 * construction, not by convention.
 *
 * Two filters run here, cheapest first:
 *
 *   1. Flow filters -- IPv4, TCP, address, fragment, port. Header reads only.
 *   2. A trace gate -- scans the first HEADER_WINDOW payload bytes of an HTTP
 *      request head for a W3C traceparent, buckets the trace id with the same
 *      FNV-1a rule user space uses, and drops what would be sampled out
 *      anyway. Sampled-in flows are recorded in an LRU keyed on the canonical
 *      5-tuple, so their continuation segments and their response half pass
 *      without re-scanning.
 *
 * The gate uses bpf_loop, so this program needs kernel >= 5.17. An older
 * kernel fails the load and the DaemonSet pod never attaches, which is the
 * intended outcome: the node keeps running, uncaptured.
 *
 * The gate exists because at 100k+ RPS with a 10% sample, nine tenths of
 * matched production traffic would otherwise cross the perf ring, get
 * reassembled by gopacket and HTTP-parsed only to be discarded.
 *
 * User space stays authoritative. The invariant that makes that safe is that
 * this program is over-permissive or exactly equal, never stricter: it drops
 * only on a *successfully parsed* trace id that buckets out. Every parse
 * failure -- no traceparent, header past the window, header split across
 * segments, malformed id -- passes the packet up and lets pkg/sample decide.
 * A drop here can therefore never lose traffic user space would have kept.
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
#define TCP_DOFF_MIN 20
#define TCP_DOFF_MAX 60

/* TCP flags byte (offset 13 into the TCP header), low 6 bits. */
#define TCP_FIN 0x01
#define TCP_SYN 0x02
#define TCP_RST 0x04

/* Payload bytes scanned for a traceparent. 768 covers a request line plus the
 * headers a real client emits ahead of traceparent -- Host, User-Agent,
 * Accept, and a moderate Cookie.
 *
 * ponytail: a traceparent past this window is not found, so the request fails
 * open and crosses to user space ungated. That is the safe direction (user
 * space re-gates) but it spends ring bandwidth. Upgrade path: raise the
 * window and the ladder in load_header() together.
 */
#define HEADER_WINDOW 768

/* Smallest ladder tier. Sized to the request line rather than to a whole
 * traceparent: a payload too short to hold one still has to be recognised as a
 * request head, because a head that cannot be gated must fail open rather than
 * fall through to the drop below.
 */
#define HEADER_MIN 32

/* Length of the 32-hex W3C trace id, and of its 16 raw bytes. */
#define TRACE_ID_HEX 32
#define TRACE_ID_LEN 16

/* FNV-1a-64. Must stay identical to pipeline/pkg/sample: the two halves gate
 * the same traffic and a divergence drops what user space would keep.
 */
#define FNV_BASIS 0xcbf29ce484222325ULL
#define FNV_PRIME 0x100000001b3ULL

/* IPv4 addresses to capture, in HOST order: 10.99.0.2 is 0x0A630002.
 * A packet matches on either source or destination.
 *
 * The value is that address's KaiselRule samplePercentage, not a presence
 * flag: samplePercentage is per-ShadowTest, so it cannot be a .rodata
 * constant. 0 and >=100 both mean "no gate", matching sample.SampledIn's
 * N<=0 || N>=100 short-circuit.
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

/* Packets the trace gate dropped, per CPU. Sampling out is intended, but a
 * gate that silently swallows everything looks identical to a gate that
 * works, so user space logs the rate.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __u64);
} sample_drops SEC(".maps");

/* One TCP connection, in the same orientation from either direction.
 *
 * Canonical rather than directional for the same reason decode.connKey orders
 * its endpoints: one entry then covers a request's continuation segments and
 * its response half, so the gate runs once per request rather than once per
 * packet.
 */
struct flow_key {
	__u32 lo_addr;
	__u32 hi_addr;
	__u16 lo_port;
	__u16 hi_port;
};

/* Connections whose current request sampled in. Entries are written by the
 * gate, replaced or removed when a new request head arrives on the same
 * connection, and dropped on SYN -- a SYN means a new connection, which is
 * what makes recycled ephemeral ports behind SNAT exact rather than merely
 * unlikely.
 *
 * LRU rather than HASH so a node that runs out of entries evicts its coldest
 * flow instead of failing the update: eviction costs an ungated request, a
 * full map would cost every request on the new flow. max_entries is set from
 * user space before load, so dense nodes size it without recompiling.
 */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 32768);
	__type(key, struct flow_key);
	__type(value, __u8);
} admitted SEC(".maps");

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

/* Header scan window. Per-CPU for the same reason as chunk_buf: the window
 * cannot live on BPF's 512-byte stack.
 *
 * HDR_SCAN_MASK bounds any index derived from the payload, and HDR_BUF_SIZE
 * leaves a full mask-width above it so a masked base can still be indexed by a
 * further constant -- the 13 bytes of "\ntraceparent:", or the 33 of a trace id
 * and its terminator -- and stay provably in bounds. Masking is what lets the
 * verifier prove each access in one step instead of tracking a derived offset
 * through the scan and the hex decode.
 *
 * Deliberately never cleared between packets. Stale bytes from this CPU's
 * previous packet are unreachable because every read is bounded by the tier
 * length load_header() actually loaded, never by sizeof(data) -- and BPF runs
 * with preemption disabled, so two invocations cannot interleave on one CPU.
 * Zeroing this per matched frame at 100k RPS would cost more than the gate
 * saves.
 */
#define HDR_SCAN_MASK 1023
#define HDR_BUF_SIZE 2048
#define HDR_MASK (HDR_BUF_SIZE - 1)

struct hdr_scratch {
	__u8 data[HDR_BUF_SIZE];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, struct hdr_scratch);
} hdr_buf SEC(".maps");

/* Link-layer header size: 14 for Ethernet, 0 for raw L3 (ARPHRD_NONE tunnels).
 * Set from user space before load, so the verifier folds it to a constant.
 * Mirrors decode.Framing on the Go side.
 */
volatile const __u32 l2_off = 14;

/* 0 = capture every TCP port, 1 = consult target_ports. A .rodata const rather
 * than a map emptiness check, which BPF cannot express.
 */
volatile const __u32 port_filter_on = 0;

/* Set from user space to lo's real ifindex when the raw socket is bound to
 * ifindex 0 ("any" -- every interface in the socket's netns, not just the one
 * physical NIC). Loopback frames carry no Ethernet header, unlike everything
 * else multiplexed onto that socket, so they cannot share l2_off with the
 * rest and are dropped outright rather than misread. 0 (the default, and
 * never a real ifindex) matches nothing, so this is a no-op when capturing on
 * a single named interface.
 */
volatile const __u32 lo_ifindex = 0;

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

/* Order the endpoints so both directions of one connection hash to a single
 * entry. Same reasoning as decode.connKey: a hash that merely collides with
 * its reverse would be cheaper, but splicing two unrelated connections is
 * silently wrong, and ordering cannot collide.
 */
static __inline void flow_of(struct flow_key *k, __u32 saddr, __u32 daddr,
			     __u16 sport, __u16 dport)
{
	if (saddr < daddr || (saddr == daddr && sport <= dport)) {
		k->lo_addr = saddr;
		k->hi_addr = daddr;
		k->lo_port = sport;
		k->hi_port = dport;
	} else {
		k->lo_addr = daddr;
		k->hi_addr = saddr;
		k->lo_port = dport;
		k->hi_port = sport;
	}
}

/* Copy the head of the payload into scratch, returning how many bytes landed.
 *
 * bpf_skb_load_bytes declares its length ARG_CONST_SIZE, so the length must be
 * a literal at every call site -- a variable holding 768 will not verify.
 * Hence a ladder rather than a clamp: pick the largest tier that fits, and read
 * exactly that many bytes.
 *
 * A tier can only be at or below the payload length, so whatever sits between
 * them goes unread. That gap is not a rounding detail: a traceparent is usually
 * the last header, ending two bytes before the payload does, so on a plain curl
 * request (~158 bytes, tier 128) it lands squarely in the gap. No granularity
 * fixes that -- the caller reads the tail separately instead, and the tiers stay
 * coarse. Only one branch runs per packet.
 */
#define LOAD_TIER(sz)                                                         \
	if (avail >= (sz))                                                    \
	return bpf_skb_load_bytes(skb, off, buf->data, (sz)) == 0 ? (sz) : 0

static __inline __u32 load_header(struct __sk_buff *skb, __u32 off, __u32 avail,
				  struct hdr_scratch *buf)
{
	LOAD_TIER(768);
	LOAD_TIER(704);
	LOAD_TIER(640);
	LOAD_TIER(576);
	LOAD_TIER(512);
	LOAD_TIER(448);
	LOAD_TIER(384);
	LOAD_TIER(320);
	LOAD_TIER(256);
	LOAD_TIER(192);
	LOAD_TIER(128);
	LOAD_TIER(96);
	LOAD_TIER(64);
	LOAD_TIER(HEADER_MIN);
	return 0;
}

#undef LOAD_TIER

/* An HTTP request head starts a new gate decision. The trailing space is what
 * makes this safe to run on arbitrary payload: without it, any binary segment
 * whose first bytes happen to read as G-E-T would enter the scan.
 */
static __inline int is_request_head(const __u8 *d, __u32 n)
{
	if (n < 8)
		return 0;
	if (d[0] == 'G' && d[1] == 'E' && d[2] == 'T' && d[3] == ' ')
		return 1;
	if (d[0] == 'P' && d[1] == 'U' && d[2] == 'T' && d[3] == ' ')
		return 1;
	if (d[0] == 'P' && d[1] == 'O' && d[2] == 'S' && d[3] == 'T' &&
	    d[4] == ' ')
		return 1;
	if (d[0] == 'H' && d[1] == 'E' && d[2] == 'A' && d[3] == 'D' &&
	    d[4] == ' ')
		return 1;
	if (d[0] == 'P' && d[1] == 'A' && d[2] == 'T' && d[3] == 'C' &&
	    d[4] == 'H' && d[5] == ' ')
		return 1;
	if (d[0] == 'D' && d[1] == 'E' && d[2] == 'L' && d[3] == 'E' &&
	    d[4] == 'T' && d[5] == 'E' && d[6] == ' ')
		return 1;
	if (d[0] == 'O' && d[1] == 'P' && d[2] == 'T' && d[3] == 'I' &&
	    d[4] == 'O' && d[5] == 'N' && d[6] == 'S' && d[7] == ' ')
		return 1;
	return 0;
}

/* Case-insensitive "traceparent:" at d. HTTP field names are case-insensitive
 * and net/http canonicalises on the Go side, so matching only the lowercase
 * spelling here would make this stricter than user space.
 */
static __inline __u32 tp_diff(const __u8 *d)
{
	__u32 diff;

	/* Accumulated into one difference rather than short-circuited: twelve
	 * early exits would be twelve branches in the hottest path of the scan,
	 * and the redundant loads are cheaper than the mispredicts.
	 */
	diff = (__u32)((d[0] | 0x20) ^ 't');
	diff |= (__u32)((d[1] | 0x20) ^ 'r');
	diff |= (__u32)((d[2] | 0x20) ^ 'a');
	diff |= (__u32)((d[3] | 0x20) ^ 'c');
	diff |= (__u32)((d[4] | 0x20) ^ 'e');
	diff |= (__u32)((d[5] | 0x20) ^ 'p');
	diff |= (__u32)((d[6] | 0x20) ^ 'a');
	diff |= (__u32)((d[7] | 0x20) ^ 'r');
	diff |= (__u32)((d[8] | 0x20) ^ 'e');
	diff |= (__u32)((d[9] | 0x20) ^ 'n');
	diff |= (__u32)((d[10] | 0x20) ^ 't');
	diff |= (__u32)(d[11] ^ ':');
	return diff;
}

/* Per-scan state handed to scan_cb through bpf_loop's callback context. */
struct scan_ctx {
	const __u8 *d;
	__u32 n;
	__s32 found;
};

/* One candidate offset. Returns non-zero to end the scan.
 *
 * Anchored on the LF that ends the preceding header line: a field name only
 * ever begins at a line boundary, so the 12-byte compare runs at the ~15 line
 * starts a request head actually contains rather than at every offset. The
 * request line occupies the first line, so requiring a preceding LF misses
 * nothing.
 */
static long scan_cb(__u32 i, void *pctx)
{
	struct scan_ctx *c = pctx;
	const __u8 *p;

	/* Past what load_header() actually read. The buffer beyond n still holds
	 * this CPU's previous packet, and matching that would gate a request on
	 * a stale trace id.
	 */
	if (i + 13 > c->n)
		return 1;

	p = c->d + (i & HDR_SCAN_MASK);
	if (p[0] != '\n')
		return 0;
	if (tp_diff(p + 1) != 0)
		return 0;

	c->found = (__s32)(i + 1);
	return 1;
}

/* Index of "traceparent:" within the loaded window, or -1.
 *
 * bpf_loop rather than a C loop, and that is what makes a window this wide
 * verifiable at all. The verifier simulates every iteration of an ordinary
 * bounded loop and cannot prune across the back-edge, because the induction
 * variable is live; measured on this program that capped the window at 160
 * bytes even with a fully branchless body, and unrolling instead spills the
 * 512-byte stack. bpf_loop verifies scan_cb exactly once no matter how many
 * times it runs, so cost stops scaling with HEADER_WINDOW.
 *
 * Scanning only -- the value is parsed in trace_id_at() below.
 */
static __inline int find_tp(const __u8 *d, __u32 n)
{
	struct scan_ctx ctx = { d, n, -1 };

	if (bpf_loop(HEADER_WINDOW - 12, scan_cb, &ctx, 0) < 0)
		return -1;
	if (ctx.found < 0 || ctx.found >= HEADER_WINDOW)
		return -1;
	return ctx.found;
}

/* Offset of the 32-hex trace id within the loaded window, or -1.
 *
 * Mirrors sample.TraceIDFromTraceparent: split on '-', take field 1, require
 * it to be 32 chars. Structural failures return -1, which the caller treats as
 * "cannot decide" and fails open -- never as "drop".
 */
static __inline int trace_id_at(const __u8 *d, __u32 n)
{
	__u32 j, k, start;
	int at;

	if (n > HEADER_WINDOW)
		n = HEADER_WINDOW;

	at = find_tp(d, n);
	if (at < 0)
		return -1;

	/* Optional whitespace after the colon, then the version field and the
	 * '-' that ends it. Bounded tightly: a well-formed value has the
	 * separator within a few bytes.
	 */
	j = (__u32)at + 12;
	for (k = 0; k < 8; k++) {
		if (j + k >= n)
			return -1;
		if (d[(j + k) & HDR_MASK] == '-')
			break;
	}
	if (k == 8)
		return -1;

	start = j + k + 1;
	/* The id itself, plus the '-' that terminates the field. */
	if (start + TRACE_ID_HEX >= n)
		return -1;
	if (d[(start + TRACE_ID_HEX) & HDR_MASK] != '-')
		return -1;
	return (int)start;
}

static __inline int hexval(__u8 c)
{
	if (c >= '0' && c <= '9')
		return c - '0';
	if (c >= 'a' && c <= 'f')
		return c - 'a' + 10;
	if (c >= 'A' && c <= 'F')
		return c - 'A' + 10;
	return -1;
}

/* Port of sample.SampledIn: FNV-1a-64 over the 16 decoded bytes, keep iff
 * (hash & 0xff) * 100 < pct * 256.
 *
 * Returns 1 keep, 0 drop, -1 undecidable. The two halves must agree exactly;
 * pipeline/pkg/sample/sample_test.go's golden vectors pin both.
 */
static __inline int sampled_in(const __u8 *d, __u32 start, __u32 pct)
{
	__u64 h = FNV_BASIS;
	int hi, lo;
	__u32 m;

#pragma unroll
	for (m = 0; m < TRACE_ID_LEN; m++) {
		hi = hexval(d[(start + m * 2) & HDR_MASK]);
		lo = hexval(d[(start + m * 2 + 1) & HDR_MASK]);
		if (hi < 0 || lo < 0)
			return -1;
		h ^= (__u64)(((__u32)hi << 4) | (__u32)lo);
		h *= FNV_PRIME;
	}
	return (h & 0xff) * 100 < (__u64)pct * 256;
}

static __inline void count_sample_drop(void)
{
	__u32 zero = 0;
	__u64 *n = bpf_map_lookup_elem(&sample_drops, &zero);

	if (n)
		(*n)++;
}

SEC("socket")
int capture(struct __sk_buff *skb)
{
	__u8 ver_ihl, proto, a[8], f[2], p[4], t[2], admit = 1, *pv;
	__u64 *frags;
	__u32 saddr, daddr, ihl, doff, total, off, pct, poff, plen, hlen, want;
	__u32 zero = 0;
	__u16 sport, dport;
	struct chunk_buf *buf;
	struct hdr_scratch *hdr;
	struct flow_key key;
	int i, last, at, keep;

	if (skb->ifindex == lo_ifindex)
		return 0;

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

	/* Effective sample percentage for this packet: the max over whichever
	 * sides matched. Ingress matches on destination and egress on source, so
	 * a packet between two target pods is governed by two rules at once; the
	 * max is the permissive choice, and this program must never be stricter
	 * than user space. A matched side storing 0 means "unset", which
	 * export.Router.Rebuild normalises to 100 -- mirror that here rather than
	 * letting 0 read as "sample nothing".
	 */
	pct = 0;
	pv = bpf_map_lookup_elem(&target_ips, &saddr);
	if (pv)
		pct = *pv ? *pv : 100;
	pv = bpf_map_lookup_elem(&target_ips, &daddr);
	if (pv) {
		__u32 v = *pv ? *pv : 100;

		if (v > pct)
			pct = v;
	}
	if (!pct)
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

	/* Data offset (high nibble of byte 12, in 4-byte words) and flags (byte
	 * 13). Validated like ihl above: an unchecked doff yields a payload
	 * offset pointing anywhere.
	 */
	if (bpf_skb_load_bytes(skb, l2_off + ihl + 12, t, sizeof(t)) < 0)
		return 0;
	doff = (t[0] >> 4) * 4;
	if (doff < TCP_DOFF_MIN || doff > TCP_DOFF_MAX)
		return 0;

	poff = l2_off + ihl + doff;
	/* Before subtracting: plen is __u32, so a truncated or malformed segment
	 * would underflow to ~4G and pass every length test below it.
	 *
	 * Strictly greater, not >=: poff == total is a payload-free segment, which
	 * is the normal case for SYN, FIN and pure ACKs. Rejecting those here would
	 * skip both the SYN invalidation and the FIN pass below.
	 */
	if (poff > total)
		return 0;
	plen = total - poff;
	/* Redundant given the checks above, but it states the bound at the point
	 * the offset is handed to a helper.
	 */
	poff &= 0x0fff;

	flow_of(&key, saddr, daddr, sport, dport);

	/* A SYN is a new connection by definition, so any entry under this
	 * 5-tuple describes a connection that no longer exists. Clearing it here
	 * is what makes recycled ephemeral ports behind SNAT exact rather than
	 * merely unlikely to alias.
	 */
	if (t[1] & TCP_SYN)
		bpf_map_delete_elem(&admitted, &key);

	/* Pure ACKs carry nothing to gate and nothing to reassemble. FIN and RST
	 * are kept even though they are payload-free: decode.runResponses frames
	 * a `Connection: close` body by reading to EOF, and EOF is the FIN.
	 */
	if (plen == 0 && !(t[1] & (TCP_SYN | TCP_FIN | TCP_RST)))
		return 0;

	/* Everything below is the trace gate. Skipped entirely when sampling is
	 * off, which is also what keeps untraced traffic (and the integration
	 * lab, which curls without a traceparent) flowing untouched.
	 */
	if (pct < 100 && plen > 0) {
		hdr = bpf_map_lookup_elem(&hdr_buf, &zero);
		if (!hdr)
			return 0;

		hlen = load_header(skb, poff, plen, hdr);
		if (hlen == 0) {
			/* Shorter than the smallest tier, or the read failed.
			 * Nothing was examined, so nothing can be concluded --
			 * and a request head that cannot be gated has to fail
			 * open rather than fall through to the drop below.
			 */
		} else if (is_request_head(hdr->data, hlen)) {
			/* A request head re-decides the connection, so a
			 * keep-alive stream is sampled per request rather than
			 * inheriting whatever its first request got.
			 */
			at = trace_id_at(hdr->data, hlen);
			if (at < 0) {
				/* The tier sits at or below plen, so the bytes
				 * between them went unread -- and a traceparent
				 * is typically the last header, ending two bytes
				 * before the payload does, squarely inside that
				 * gap. Re-read the same tier aligned to the end
				 * of the window instead of its start. The two
				 * reads overlap and together cover everything up
				 * to HEADER_WINDOW, which is what makes the
				 * window mean what it says.
				 */
				want = plen < HEADER_WINDOW ? plen : HEADER_WINDOW;
				if (want > hlen &&
				    load_header(skb, poff + (want - hlen), hlen,
						hdr) == hlen)
					at = trace_id_at(hdr->data, hlen);
			}
			keep = at < 0 ? -1 : sampled_in(hdr->data, (__u32)at, pct);
			if (keep == 0) {
				bpf_map_delete_elem(&admitted, &key);
				count_sample_drop();
				return 0;
			}
			/* keep == 1 sampled in; keep == -1 could not be
			 * decided (no traceparent, header past the window, or
			 * split across segments) and fails open. Both admit
			 * the flow: the continuation segments and the response
			 * half must follow the head they belong to, and user
			 * space re-gates either way.
			 */
			bpf_map_update_elem(&admitted, &key, &admit, BPF_ANY);
		} else if (!bpf_map_lookup_elem(&admitted, &key)) {
			/* Not a head, and its connection was never admitted:
			 * a continuation or response belonging to a request
			 * this gate already dropped.
			 */
			count_sample_drop();
			return 0;
		}
	}

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
