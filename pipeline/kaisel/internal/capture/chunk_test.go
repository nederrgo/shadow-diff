package capture

import (
	"bytes"
	"testing"
)

// chunksFor splits payload the way capture.c does: fixed-size chunks, with the
// final one backwards-aligned to total-CHUNK so every kernel-side read is a
// compile-time-constant length.
func chunksFor(t *testing.T, payload []byte) []struct {
	meta bpfPktMeta
	data []byte
} {
	t.Helper()
	total := uint32(len(payload))
	var out []struct {
		meta bpfPktMeta
		data []byte
	}

	if total <= chunkSize {
		out = append(out, struct {
			meta bpfPktMeta
			data []byte
		}{bpfPktMeta{Len: total, OrigLen: total, Offset: 0, More: 0}, payload})
		return out
	}

	for i := 0; i < maxChunks; i++ {
		off := uint32(i) * chunkSize
		if off >= total {
			break
		}
		last := off+chunkSize >= total || i == maxChunks-1
		if last {
			off = total - chunkSize
		}
		more := uint32(1)
		if last {
			more = 0
		}
		out = append(out, struct {
			meta bpfPktMeta
			data []byte
		}{
			bpfPktMeta{Len: chunkSize, OrigLen: total, Offset: off, More: more},
			payload[off : off+chunkSize],
		})
		if last {
			break
		}
	}
	return out
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251) // non-repeating enough to catch misplacement
	}
	return b
}

func TestReassembleRoundTrip(t *testing.T) {
	// 62557 is the largest packet actually measured on this host during a
	// 200KB POST -- the case that motivated chunking.
	for _, size := range []int{1, 100, chunkSize - 1, chunkSize, chunkSize + 1,
		2*chunkSize + 7, 62557, maxPacketBytes} {
		payload := pattern(size)
		r := newReassembler()

		var got []byte
		var done, trunc bool
		for _, c := range chunksFor(t, payload) {
			var origLen uint32
			got, origLen, trunc, done = r.push(0, c.meta, c.data)
			if done && origLen != uint32(size) {
				t.Fatalf("size %d: origLen = %d", size, origLen)
			}
		}
		if !done {
			t.Fatalf("size %d: never completed", size)
		}
		if trunc {
			t.Errorf("size %d: reported truncated", size)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("size %d: payload mismatch (got %d bytes)", size, len(got))
		}
		if len(r.perCPU) != 0 {
			t.Errorf("size %d: partial leaked", size)
		}
	}
}

// The backwards-aligned final chunk overlaps its predecessor. Reassembly must
// place chunks by absolute offset so the overlap rewrites identical bytes.
func TestReassembleOverlapIsHarmless(t *testing.T) {
	payload := pattern(chunkSize + 100)
	cs := chunksFor(t, payload)
	if len(cs) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(cs))
	}
	if cs[1].meta.Offset != 100 {
		t.Fatalf("final chunk offset = %d, want 100 (backwards-aligned)", cs[1].meta.Offset)
	}

	r := newReassembler()
	r.push(0, cs[0].meta, cs[0].data)
	got, _, trunc, done := r.push(0, cs[1].meta, cs[1].data)
	if !done || trunc || !bytes.Equal(got, payload) {
		t.Error("overlapping final chunk did not reassemble cleanly")
	}
}

// A lost sample makes continuity unprovable; the partial must be dropped rather
// than emitted half-filled.
func TestLostSamplesDiscardsPartial(t *testing.T) {
	payload := pattern(3 * chunkSize)
	cs := chunksFor(t, payload)

	r := newReassembler()
	r.push(0, cs[0].meta, cs[0].data)
	if len(r.perCPU) != 1 {
		t.Fatal("expected an open partial")
	}

	r.dropCPU(0)
	if len(r.perCPU) != 0 {
		t.Error("dropCPU left the partial in place")
	}
	if r.discarded != 1 {
		t.Errorf("discarded = %d, want 1", r.discarded)
	}

	// The remaining chunks have no start and must not resurrect the packet.
	for _, c := range cs[1:] {
		if _, _, _, done := r.push(0, c.meta, c.data); done {
			t.Fatal("emitted a packet assembled from orphaned chunks")
		}
	}
}

func TestNewPacketDropsStalePartial(t *testing.T) {
	first := chunksFor(t, pattern(3*chunkSize))
	second := pattern(2 * chunkSize)

	r := newReassembler()
	r.push(0, first[0].meta, first[0].data) // start, then never finish

	var got []byte
	var done bool
	for _, c := range chunksFor(t, second) {
		got, _, _, done = r.push(0, c.meta, c.data)
	}
	if !done || !bytes.Equal(got, second) {
		t.Error("second packet did not reassemble after an abandoned first")
	}
	if r.discarded != 1 {
		t.Errorf("discarded = %d, want 1 for the abandoned packet", r.discarded)
	}
}

func TestOversizedRejected(t *testing.T) {
	r := newReassembler()
	_, _, _, done := r.push(0, bpfPktMeta{
		Len: chunkSize, OrigLen: maxPacketBytes + 1, Offset: 0, More: 1,
	}, make([]byte, chunkSize))

	if done {
		t.Error("accepted a packet past the compile-time ceiling")
	}
	if r.oversized != 1 {
		t.Errorf("oversized = %d, want 1", r.oversized)
	}
	if len(r.perCPU) != 0 {
		t.Error("allocated a buffer for an oversized packet")
	}
}

// A packet larger than CHUNK*MAX_CHUNKS gets terminated by the kernel's forced
// last flag, so it arrives complete-looking but short. That must surface as
// truncated rather than passing as a whole body.
func TestIncompleteCoverageReportsTruncated(t *testing.T) {
	const total = 4 * chunkSize
	r := newReassembler()

	r.push(0, bpfPktMeta{Len: chunkSize, OrigLen: total, Offset: 0, More: 1}, pattern(chunkSize))
	// Jump straight to a terminating chunk, leaving a hole.
	_, _, trunc, done := r.push(0, bpfPktMeta{
		Len: chunkSize, OrigLen: total, Offset: chunkSize, More: 0,
	}, pattern(chunkSize))

	if !done {
		t.Fatal("expected completion on more == 0")
	}
	if !trunc {
		t.Error("short coverage not reported as truncated")
	}
}

// Chunks from different CPUs are independent packets and must not interleave.
func TestPerCPUIsolation(t *testing.T) {
	a, b := pattern(2*chunkSize), pattern(3*chunkSize)
	for i := range b {
		b[i] ^= 0xff
	}
	ca, cb := chunksFor(t, a), chunksFor(t, b)

	r := newReassembler()
	var gotA, gotB []byte
	// Interleave the two CPUs' streams.
	for i := 0; i < len(ca) || i < len(cb); i++ {
		if i < len(ca) {
			if out, _, _, done := r.push(0, ca[i].meta, ca[i].data); done {
				gotA = append([]byte(nil), out...)
			}
		}
		if i < len(cb) {
			if out, _, _, done := r.push(1, cb[i].meta, cb[i].data); done {
				gotB = append([]byte(nil), out...)
			}
		}
	}
	if !bytes.Equal(gotA, a) {
		t.Error("CPU 0 packet corrupted by interleaving")
	}
	if !bytes.Equal(gotB, b) {
		t.Error("CPU 1 packet corrupted by interleaving")
	}
	if r.discarded != 0 {
		t.Errorf("discarded = %d, want 0", r.discarded)
	}
}
