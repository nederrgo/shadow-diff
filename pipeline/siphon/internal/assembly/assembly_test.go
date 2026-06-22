package assembly

import (
	"testing"

	"github.com/google/gopacket/tcpassembly"
)

type mockStream struct {
	writes int
	bytes  int
}

func (m *mockStream) Reassembled(reassemblies []tcpassembly.Reassembly) {
	for _, r := range reassemblies {
		if len(r.Bytes) == 0 {
			continue
		}
		m.writes++
		m.bytes += len(r.Bytes)
	}
}

func (m *mockStream) ReassemblyComplete() {}

func TestCappedStream_discardsAfterMaxBytes(t *testing.T) {
	inner := &mockStream{}
	cap := newCappedStream(inner, "test-flow", nil)
	cap.maxBytes = 100

	cap.Reassembled([]tcpassembly.Reassembly{{Bytes: make([]byte, 60)}})
	cap.Reassembled([]tcpassembly.Reassembly{{Bytes: make([]byte, 50)}})

	if !cap.discarded.Load() {
		t.Fatal("expected stream to be discarded")
	}
	if inner.bytes != 60 {
		t.Fatalf("inner bytes = %d, want 60 (overflow chunk must not be forwarded)", inner.bytes)
	}

	writesBefore := inner.writes
	cap.Reassembled([]tcpassembly.Reassembly{{Bytes: make([]byte, 1000)}})
	if inner.writes != writesBefore {
		t.Fatal("inner stream received writes after discard")
	}
}
