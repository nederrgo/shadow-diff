package proxy

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// copyBufferBytes is the unit of socket-to-socket movement. Buffers are pooled
// rather than allocated per connection so a burst of short-lived database
// connections does not churn the heap and drag GC latency into the application's
// query path.
const copyBufferBytes = 32 * 1024

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, copyBufferBytes)
		return &b
	},
}

// pipe moves bytes from src to dst using a pooled buffer.
func pipe(dst io.Writer, src io.Reader) (int64, error) {
	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)
	return io.CopyBuffer(dst, src, *buf)
}

// tap is a bounded, non-blocking side-channel from the proxy's copy loop to a
// protocol parser.
//
// It must never block: the writer is the goroutine carrying the application's
// own database traffic, so a parser that reads slowly has to lose data rather
// than stall a query. This is the same constraint Kaisel documents for its egress
// response side — a synchronous handoff deadlocks the thing it is observing.
//
// On overflow the tap closes itself instead of dropping a single chunk. A gap in
// the middle of a stream leaves the parser's framing unrecoverable, so every
// report after it would be garbage; ending cleanly and counting one overflow is
// both honest and cheaper than emitting nonsense.
type tap struct {
	ch       chan []byte
	cur      []byte
	overflow atomic.Bool
	once     sync.Once
}

func newTap(chunks int) *tap {
	return &tap{ch: make(chan []byte, chunks)}
}

// Write never blocks and never fails.
func (t *tap) Write(p []byte) (int, error) {
	if t.overflow.Load() {
		return len(p), nil
	}
	// The caller's buffer comes from bufferPool and is reused as soon as this
	// returns, so the chunk must be copied before it is queued.
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case t.ch <- b:
	default:
		t.overflow.Store(true)
		t.close()
	}
	return len(p), nil
}

func (t *tap) Read(p []byte) (int, error) {
	for len(t.cur) == 0 {
		b, ok := <-t.ch
		if !ok {
			return 0, io.EOF
		}
		t.cur = b
	}
	n := copy(p, t.cur)
	t.cur = t.cur[n:]
	return n, nil
}

func (t *tap) close() { t.once.Do(func() { close(t.ch) }) }

// Overflowed reports whether the tap dropped its stream.
func (t *tap) Overflowed() bool { return t.overflow.Load() }

// idleConn closes a connection that has gone quiet, so an abandoned client
// cannot hold a proxy goroutine and an upstream socket open forever.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(b []byte) (int, error) {
	if c.idle > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Read(b)
}

func (c *idleConn) Write(b []byte) (int, error) {
	if c.idle > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Write(b)
}
