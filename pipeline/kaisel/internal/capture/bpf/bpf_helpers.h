/* Minimal vendored BPF helper definitions.
 *
 * Deliberately includes no system headers. libbpf's bpf_helpers.h pulls in
 * <linux/bpf.h>, which ties the build to the host's linux-libc-dev vintage --
 * on an older distro that header predates the map types and flags used here.
 * Vendoring the handful of definitions we actually need makes the object
 * reproducible on any box with a clang that can target BPF, with no
 * build-time system dependency, no bpftool and no vmlinux.h.
 *
 * Everything below is stable UAPI: the values are fixed by the kernel ABI and
 * cannot change without breaking every existing BPF program.
 */
#ifndef __KAISEL_BPF_HELPERS_H
#define __KAISEL_BPF_HELPERS_H

typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
typedef int __s32;

#define SEC(name) __attribute__((section(name), used))

/* BTF-defined map declaration macros (libbpf convention). */
#define __uint(name, val) int(*name)[val]
#define __type(name, val) typeof(val) *name

/* enum bpf_map_type */
#define BPF_MAP_TYPE_HASH 1
#define BPF_MAP_TYPE_PERF_EVENT_ARRAY 4
#define BPF_MAP_TYPE_PERCPU_ARRAY 6
#define BPF_MAP_TYPE_LRU_HASH 9

/* bpf_map_update_elem flags. BPF_ANY creates or overwrites. */
#define BPF_ANY 0

/* Write to the perf buffer of whichever CPU the program is running on. */
#define BPF_F_CURRENT_CPU 0xffffffffULL

/* Partial struct __sk_buff. len really is at offset 0, and the verifier
 * rewrites field access by offset, so a truncated definition is safe as long
 * as any field added later is declared in real UAPI order. ifindex is the
 * 11th __u32 (offset 40) per linux/bpf.h -- the 9 fields between len and
 * ifindex are unused padding, present only so the offset lines up.
 */
struct __sk_buff {
	__u32 len;
	__u32 pkt_type;
	__u32 mark;
	__u32 queue_mapping;
	__u32 protocol;
	__u32 vlan_present;
	__u32 vlan_tci;
	__u32 vlan_proto;
	__u32 priority;
	__u32 ingress_ifindex;
	__u32 ifindex;
};

/* Helpers by their fixed ABI ids, the pre-libbpf calling convention. */
static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key,
				   const void *value, __u64 flags) = (void *)2;
static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *)3;
static long (*bpf_perf_event_output)(void *ctx, void *map, __u64 flags,
				     void *data, __u64 size) = (void *)25;
static long (*bpf_skb_load_bytes)(const void *skb, __u32 offset, void *to,
				  __u32 len) = (void *)26;
/* Calls callback_fn(index, callback_ctx) nr_loops times, stopping early when
 * the callback returns non-zero. The verifier checks the callback once instead
 * of simulating every iteration, which is the only way a scan this wide fits
 * inside the 1M instruction ceiling. Kernel >= 5.17.
 */
static long (*bpf_loop)(__u32 nr_loops, void *callback_fn, void *callback_ctx,
			__u64 flags) = (void *)181;

#endif /* __KAISEL_BPF_HELPERS_H */
