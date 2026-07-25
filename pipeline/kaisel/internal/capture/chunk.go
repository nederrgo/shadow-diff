package capture

// Chunk reassembly.
//
// GSO hands AF_PACKET packets far larger than the MTU -- 62KB observed on a
// 1500-byte-MTU path -- while a perf sample is hard-capped at 65535 bytes by
// the __u16 size field in struct perf_event_header. capture.c therefore splits
// oversized packets into fixed CHUNK-sized events, and this reassembles them.
//
// Correlation needs no flow key: a packet is processed start to finish on one
// CPU, so its chunks land consecutively in that CPU's ring, in order.
// perf.Record carries the CPU, which is all the state required.
//
// Not safe for concurrent use. The capture loop is single-goroutine.

// Must match the constants in bpf/capture.c.
const (
	chunkSize      = 4096
	maxChunks      = 32
	maxPacketBytes = chunkSize * maxChunks // 128KB ceiling, baked into the object
)

type partial struct {
	buf     []byte
	origLen uint32
	// covered is the highest offset+len written. Chunks are contiguous or
	// overlapping and arrive in order, so this is a sound completeness
	// measure -- and it doubles as truncation detection, which is why
	// pkt_meta needs no separate truncated flag.
	covered uint32
}

type reassembler struct {
	perCPU map[int]*partial

	// discarded counts packets abandoned mid-sequence. Distinct from the
	// kernel's LostSamples: "records the ring dropped" and "packets we could
	// not put back together" are different operational signals.
	discarded uint64
	// oversized counts packets beyond the compile-time ceiling.
	oversized uint64
}

func newReassembler() *reassembler {
	return &reassembler{perCPU: make(map[int]*partial)}
}

// dropCPU abandons any partial for a CPU.
//
// Called when the kernel reports lost samples. Continuity is unprovable after
// a drop, and emitting a partly-filled buffer would hand tcpassembly
// plausible-looking corrupt bytes -- reintroducing exactly the silent
// corruption chunking exists to remove. Losing the packet is the safe outcome.
func (r *reassembler) dropCPU(cpu int) {
	if _, ok := r.perCPU[cpu]; ok {
		delete(r.perCPU, cpu)
		r.discarded++
	}
}

// push feeds one chunk. done reports whether a complete packet is ready; out is
// valid only until the next push for that CPU.
func (r *reassembler) push(cpu int, m bpfPktMeta, data []byte) (out []byte, origLen uint32, truncated, done bool) {
	if m.OrigLen == 0 || m.OrigLen > maxPacketBytes {
		r.oversized++
		r.dropCPU(cpu)
		return nil, 0, false, false
	}
	if uint32(len(data)) > m.Len {
		data = data[:m.Len]
	}

	// Fast path: capture.c emits len == orig_len only for packets that fit in
	// a single event. No buffering, no allocation.
	if m.Offset == 0 && m.More == 0 && m.Len == m.OrigLen {
		r.dropCPU(cpu) // a previous partial can never complete now
		return data, m.OrigLen, uint32(len(data)) < m.OrigLen, true
	}

	p := r.perCPU[cpu]

	if m.Offset == 0 {
		// Start of a multi-chunk packet. An open partial means the previous
		// packet lost its tail.
		r.dropCPU(cpu)
		p = &partial{buf: make([]byte, m.OrigLen), origLen: m.OrigLen}
		r.perCPU[cpu] = p
	} else {
		// A continuation with no start, or for a different packet, means we
		// missed chunks; there is nothing safe to do but drop it.
		if p == nil || p.origLen != m.OrigLen {
			r.dropCPU(cpu)
			return nil, 0, false, false
		}
	}

	if m.Offset < uint32(len(p.buf)) {
		n := copy(p.buf[m.Offset:], data)
		if end := m.Offset + uint32(n); end > p.covered {
			p.covered = end
		}
	}

	if m.More != 0 {
		return nil, 0, false, false
	}

	delete(r.perCPU, cpu)
	return p.buf, p.origLen, p.covered < p.origLen, true
}
