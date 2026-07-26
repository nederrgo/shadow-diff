package decode

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gopacket/gopacket"
)

// errBufferOverflow marks a response stream that outgrew its buffer.
var errBufferOverflow = errors.New("response stream exceeds buffer limit")

const (
	// pairTimeout bounds how long the response half-stream waits for the
	// request that frames it.
	//
	// Waiting this long is only safe because runResponses drains its stream
	// through a streamBuffer: blocking here would otherwise deadlock. See
	// streamBuffer's comment for why.
	pairTimeout = 30 * time.Second

	// pairSweepAge drops connection entries idle beyond this. Mirrors the
	// flushInterval/flushAge pattern the assembler already uses.
	pairSweepAge = 2 * time.Minute

	// pendingDepth bounds requests buffered per connection awaiting a response.
	// HTTP/1.1 pipelining depth is realistically 1; a full channel means the
	// response side is gone, so the request is dropped rather than blocking
	// the assembler goroutine that feeds every other stream.
	pendingDepth = 8
)

// pendingReq is one request awaiting its response.
//
// The request body is deliberately absent: Shop's record payload has no
// request-body field and replay.TraceKey does not use one, so retaining it
// would hold production bytes in memory only to drop them. req itself is kept
// because http.ReadResponse needs it to frame the response -- HEAD carries no
// body, 204 and 304 carry no body, and only the request disambiguates.
type pendingReq struct {
	req *http.Request
}

type conn struct {
	pending  chan *pendingReq
	lastSeen time.Time
}

// connTable is the rendezvous between the two half-streams of one TCP
// connection. tcpassembly hands each direction to its own goroutine with no
// shared state, so without this the response could never find its request.
type connTable struct {
	mu    sync.Mutex
	conns map[string]*conn
	// now is swappable so eviction is testable without sleeping.
	now func() time.Time
}

func newConnTable() *connTable {
	return &connTable{conns: make(map[string]*conn), now: time.Now}
}

// connKey is the same string for both directions of one connection.
//
// gopacket's Flow.FastHash() is documented to collide with its reverse and
// would be cheaper, but a hash collision here would splice two unrelated
// connections' requests and responses together, and a mispaired mock is
// silently wrong -- it replays a real 2xx for a call that never made it.
// Ordering the endpoints costs an allocation and cannot collide.
func connKey(netFlow, transportFlow gopacket.Flow) string {
	src := netFlow.Src().String() + ":" + transportFlow.Src().String()
	dst := netFlow.Dst().String() + ":" + transportFlow.Dst().String()
	if src > dst {
		src, dst = dst, src
	}
	return src + "|" + dst
}

func (t *connTable) get(key string) *conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.conns[key]
	if !ok {
		c = &conn{pending: make(chan *pendingReq, pendingDepth)}
		t.conns[key] = c
	}
	c.lastSeen = t.now()
	return c
}

func (t *connTable) drop(key string) {
	t.mu.Lock()
	delete(t.conns, key)
	t.mu.Unlock()
}

// sweep evicts connections idle beyond pairSweepAge. Without it a table entry
// survives for every connection that ever carried a request, which on a busy
// node is an unbounded leak.
func (t *connTable) sweep() {
	cutoff := t.now().Add(-pairSweepAge)
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, c := range t.conns {
		if c.lastSeen.Before(cutoff) {
			delete(t.conns, key)
		}
	}
}

// streamBuffer decouples draining a reassembled stream from parsing it.
//
// This exists because tcpreader.ReaderStream is synchronous: the assembler's
// Reassembled call is a blocking send that only completes once the stream's
// goroutine reads it. The response parser must wait for the request that frames
// it, and that wait would otherwise stop the stream being drained -- so the
// same flush loop could no longer deliver the request half either. Response
// waits for request; response blocks request. Nothing but a timeout breaks it,
// and whenever the assembler releases both directions response-first, pairing
// fails outright.
//
// Writes therefore never block and never fail: a copier goroutine drains the
// ReaderStream into here at full speed while the parser reads at its own pace.
// Past limit the buffer marks itself overflowed and keeps accepting writes, so
// the copier still reaches EOF and the assembler still makes progress.
type streamBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
	over   bool
	limit  int
}

func newStreamBuffer(limit int) *streamBuffer {
	b := &streamBuffer{limit: limit}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *streamBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.over:
		// Already given up on this stream; swallow so the copier still drains.
	case b.buf.Len()+len(p) > b.limit:
		b.over = true
		b.buf.Reset()
		b.cond.Broadcast()
	default:
		b.buf.Write(p)
		b.cond.Broadcast()
	}
	return len(p), nil
}

func (b *streamBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && !b.closed && !b.over {
		b.cond.Wait()
	}
	if b.over {
		return 0, errBufferOverflow
	}
	if b.buf.Len() == 0 {
		return 0, io.EOF
	}
	return b.buf.Read(p)
}

func (b *streamBuffer) Close() {
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// publish hands a parsed request to the response side. It never blocks: a full
// channel means no response side is draining, so the request is dropped and
// reported rather than stalling the stream goroutine.
func (c *conn) publish(req *http.Request) bool {
	select {
	case c.pending <- &pendingReq{req: req}:
		return true
	default:
		return false
	}
}

// await returns the next request awaiting a response, or nil if none arrives
// within timeout. A response with no request cannot be framed correctly, so it
// is abandoned rather than guessed at.
func (c *conn) await(timeout time.Duration) *pendingReq {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case p := <-c.pending:
		return p
	case <-timer.C:
		return nil
	}
}
