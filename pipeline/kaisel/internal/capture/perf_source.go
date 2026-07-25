package capture

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/perf"
)

// metaSize is the wire size of struct pkt_meta, which prefixes every sample.
var metaSize = binary.Size(bpfPktMeta{})

// perfSource reads packet events from a BPF_MAP_TYPE_PERF_EVENT_ARRAY,
// reassembling chunked oversized packets on the way through.
type perfSource struct {
	rd  *perf.Reader
	rec perf.Record
	asm *reassembler
}

func newPerfSource(m *ebpf.Map, perCPUBuffer int) (*perfSource, error) {
	rd, err := perf.NewReader(m, perCPUBuffer)
	if err != nil {
		return nil, fmt.Errorf("open perf reader: %w", err)
	}
	return &perfSource{rd: rd, asm: newReassembler()}, nil
}

func (s *perfSource) Read(ev *PacketEvent) error {
	for {
		if err := s.rd.ReadInto(&s.rec); err != nil {
			if errors.Is(err, os.ErrClosed) || errors.Is(err, perf.ErrClosed) {
				return ErrClosed
			}
			return err
		}

		// A drop makes chunk continuity unprovable, so anything in flight on
		// that CPU has to go. Report the loss either way.
		if s.rec.LostSamples > 0 {
			s.asm.dropCPU(s.rec.CPU)
			if len(s.rec.RawSample) == 0 {
				*ev = PacketEvent{Lost: s.rec.LostSamples}
				return nil
			}
		}
		if len(s.rec.RawSample) < metaSize {
			continue // truncated sample; nothing decodable
		}

		var meta bpfPktMeta
		if err := binary.Read(bytes.NewReader(s.rec.RawSample[:metaSize]), binary.NativeEndian, &meta); err != nil {
			return fmt.Errorf("decode pkt_meta: %w", err)
		}

		data, origLen, truncated, done := s.asm.push(s.rec.CPU, meta, s.rec.RawSample[metaSize:])
		if !done {
			continue // mid-packet; keep draining
		}
		*ev = PacketEvent{
			Data:      data,
			OrigLen:   origLen,
			Truncated: truncated,
			Lost:      s.rec.LostSamples,
		}
		return nil
	}
}

// stats returns counters for packets the reassembler had to give up on.
func (s *perfSource) stats() (discarded, oversized uint64) {
	return s.asm.discarded, s.asm.oversized
}

func (s *perfSource) Close() error { return s.rd.Close() }
