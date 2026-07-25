package capture

import "errors"

// ErrClosed is returned by PacketSource.Read after the source is closed.
var ErrClosed = errors.New("capture: source closed")

// PacketEvent is one captured frame handed up from the kernel.
type PacketEvent struct {
	// Data is the captured bytes, framed at Framing.Offset(). Only valid until
	// the next Read: implementations may reuse the backing array.
	Data []byte
	// OrigLen is the wire length of the packet.
	OrigLen uint32
	// Truncated reports that Data is short of OrigLen -- the packet exceeded
	// the compile-time chunk ceiling. Callers must not treat a truncated body
	// as complete: partial bodies replayed into the shadow stack produce
	// green-but-vacuous test results.
	Truncated bool
	// Lost counts records the kernel dropped because the ring was full, since
	// the previous Read. Non-zero means the consumer is falling behind.
	Lost uint64
}

// PacketSource yields captured frames.
//
// The interface exists so the transport can change without touching decode or
// the HTTP layer. Today the only implementation is perf-array based, chosen
// because target node kernels may predate BPF ring buffers (5.8). A ringbuf
// source implements these same two methods when that floor rises.
type PacketSource interface {
	// Read fills ev with the next event, blocking until one arrives.
	// Returns ErrClosed once the source is closed.
	Read(ev *PacketEvent) error
	Close() error
}
