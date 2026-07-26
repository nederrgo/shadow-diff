package decode

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/tcpassembly"
	"github.com/gopacket/gopacket/tcpassembly/tcpreader"
)

// logBodyCap bounds how much captured body content a single log line can
// carry, so one oversized request can't flood the log with its full payload.
const logBodyCap = 4096

// maxEgressBodyBytes bounds a captured response body. On exceed the record is
// DROPPED, never truncated: a short body replayed as a mock into all three
// shadow roles yields identical malformed input, identical failures, a clean
// diff, and a green report that exercised only the error path. That is silent
// loss of coverage -- the failure mode nobody investigates.
const maxEgressBodyBytes = 1 << 20

// responseBufferLimit caps bytes buffered but not yet parsed on one response
// stream. Headroom above maxEgressBodyBytes covers headers and a pipelined
// response arriving behind the one being parsed.
const responseBufferLimit = maxEgressBodyBytes + (64 << 10)

// errBodyTooLarge marks a response body past the cap.
var errBodyTooLarge = errors.New("response body exceeds cap")

// responsePrefix identifies the response half of a connection. Peeking for it
// is what keeps this package target-agnostic: direction is decided by what the
// bytes are, not by which address is being captured, so it stays correct even
// when both endpoints are capture targets.
const responsePrefix = "HTTP/"

// StreamFactory reassembles TCP streams and parses HTTP out of them.
//
// OnRequest is both the test hook and the ingress export seam: main builds an
// HTTPRecord (Method, RequestURI, Host, Body, Traceparent) and POSTs to igris.
// That type lives in internal/forwarder (cannot import another module's
// internal/).
type StreamFactory struct {
	Log *slog.Logger
	// OnRequest is called for every successfully parsed request. When nil,
	// requests are logged instead.
	OnRequest func(netFlow, transportFlow gopacket.Flow, req *http.Request)
	// LogBodies includes body content (truncated to logBodyCap) in the
	// default log line. Only takes effect when OnRequest is nil.
	LogBodies bool
	// OnTransaction receives a request paired with its response, for egress
	// mock recording. Nil disables response pairing entirely, in which case
	// response streams are drained and discarded.
	OnTransaction func(netFlow, transportFlow gopacket.Flow, req *http.Request, resp *http.Response)
	// WantTransaction gates pairing per stream, consulted once with that
	// stream's own direction. Nil pairs everything. The exporter backs this
	// with a router lookup so connections with no egress route never buffer a
	// response body.
	WantTransaction func(netFlow gopacket.Flow) bool

	initOnce sync.Once
	conns    *connTable

	// oversized counts response bodies dropped for exceeding
	// maxEgressBodyBytes; unpaired counts requests dropped because no response
	// side drained them. Both would otherwise be silent losses.
	oversized atomic.Uint64
	unpaired  atomic.Uint64
}

// Oversized reports response bodies dropped for exceeding the body cap.
func (s *StreamFactory) Oversized() uint64 { return s.oversized.Load() }

// Unpaired reports requests dropped because no response side consumed them.
func (s *StreamFactory) Unpaired() uint64 { return s.unpaired.Load() }

// wantPairing reports whether a connection needs request/response pairing.
// requestFlow must be the REQUEST direction, so callers on the response half
// pass their own flow reversed.
func (s *StreamFactory) wantPairing(requestFlow gopacket.Flow) bool {
	if s.OnTransaction == nil {
		return false
	}
	return s.WantTransaction == nil || s.WantTransaction(requestFlow)
}

func (s *StreamFactory) table() *connTable {
	s.initOnce.Do(func() { s.conns = newConnTable() })
	return s.conns
}

// Sweep evicts idle connection-pairing state. Callers drive it from the same
// ticker that flushes the assembler.
func (s *StreamFactory) Sweep() {
	s.table().sweep()
}

// New implements tcpassembly.StreamFactory.
func (s *StreamFactory) New(netFlow, transportFlow gopacket.Flow) tcpassembly.Stream {
	r := tcpreader.NewReaderStream()
	go s.run(netFlow, transportFlow, &r)
	return &r
}

func (s *StreamFactory) run(netFlow, transportFlow gopacket.Flow, r *tcpreader.ReaderStream) {
	// Read rather than Peek so the bytes can be handed to either path below.
	// This blocks until the direction's first bytes arrive, which is exactly
	// when the direction becomes knowable.
	head := make([]byte, len(responsePrefix))
	n, err := io.ReadFull(r, head)
	if n == 0 {
		if err != nil {
			tcpreader.DiscardBytesToEOF(r)
		}
		return
	}
	src := io.MultiReader(bytes.NewReader(head[:n]), r)

	if string(head[:n]) == responsePrefix {
		// This stream's flow runs dependency→target, so reverse it before
		// asking: WantTransaction is defined on the REQUEST direction, where an
		// egress connection has the target pod as its source. Asking with the
		// response direction's own flow would test the dependency's address and
		// discard every response half — leaving nothing to pair with.
		if !s.wantPairing(netFlow.Reverse()) {
			// Nothing consumes responses; drain so the assembler can retire the
			// stream instead of blocking on it.
			tcpreader.DiscardBytesToEOF(r)
			return
		}
		s.runResponses(netFlow, transportFlow, src)
		return
	}
	pairing := s.wantPairing(netFlow)
	// The request side parses straight off the stream: it never waits on
	// anything but its own bytes, so it cannot stall the assembler and needs no
	// decoupling buffer (nor the size limit one would impose on request bodies).
	s.runRequests(netFlow, transportFlow, bufio.NewReader(src), r, pairing)
}

func (s *StreamFactory) runRequests(
	netFlow, transportFlow gopacket.Flow,
	buf *bufio.Reader,
	r *tcpreader.ReaderStream,
	pairing bool,
) {
	var c *conn
	if pairing {
		c = s.table().get(connKey(netFlow, transportFlow))
	}

	for {
		req, err := http.ReadRequest(buf)
		switch {
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			return
		case err != nil:
			// Not HTTP, or a truncated request at the capture snaplen. Drain so
			// the assembler can retire the stream instead of blocking on it.
			tcpreader.DiscardBytesToEOF(r)
			return
		}

		// The callback runs BEFORE the drain so it can read req.Body. This is
		// the seam ingress export uses to build HTTPRecord.Body, and it is what
		// lets a test verify a large body arrived intact.
		if s.OnRequest != nil {
			s.OnRequest(netFlow, transportFlow, req)
		} else {
			s.logRequest(netFlow, transportFlow, req)
		}

		// Drain whatever the callback left, so the next request on this stream
		// can be parsed. The response side needs only req's framing metadata.
		tcpreader.DiscardBytesToEOF(req.Body)
		req.Body.Close()

		if c != nil && !c.publish(req) {
			s.unpaired.Add(1)
			s.logger().Warn("egress request dropped; no response side draining",
				"method", req.Method, "host", req.Host, "uri", req.RequestURI,
				"total", s.unpaired.Load())
		}
	}
}

func (s *StreamFactory) runResponses(netFlow, transportFlow gopacket.Flow, src io.Reader) {
	key := connKey(netFlow, transportFlow)
	c := s.table().get(key)
	defer s.table().drop(key)

	// The copier keeps the reassembled stream draining at full speed no matter
	// how long the parser below blocks pairing. Without it, waiting for a
	// request would stop the assembler from delivering that very request.
	sb := newStreamBuffer(responseBufferLimit)
	go func() {
		_, _ = io.Copy(sb, src)
		sb.Close()
	}()
	buf := bufio.NewReader(sb)

	for {
		// Peek returns as soon as the next response starts arriving and errors
		// at EOF, so a closed connection is retired without waiting on pairing.
		if _, err := buf.Peek(1); err != nil {
			if errors.Is(err, errBufferOverflow) {
				s.oversized.Add(1)
				s.logger().Warn("egress response stream exceeds buffer limit; connection abandoned",
					"src", netFlow.Src().String(), "dst", netFlow.Dst().String(),
					"limit_bytes", responseBufferLimit, "total", s.oversized.Load())
			}
			return
		}

		pending := c.await(pairTimeout)
		if pending == nil {
			// Response bytes arrived but the request was never seen -- we missed
			// its packets. Guessing the framing would mis-read HEAD and 204/304
			// bodies, so abandon the stream instead.
			s.unpaired.Add(1)
			s.logger().Warn("egress response with no matching request; stream abandoned",
				"src", netFlow.Src().String(), "dst", netFlow.Dst().String(),
				"total", s.unpaired.Load())
			return
		}

		resp, err := http.ReadResponse(buf, pending.req)
		if err != nil {
			return
		}

		body, err := readCappedBody(resp.Body)
		resp.Body.Close()
		if err != nil {
			s.oversized.Add(1)
			s.logger().Warn("egress response body exceeds cap; record dropped rather than truncated",
				"method", pending.req.Method, "host", pending.req.Host,
				"uri", pending.req.RequestURI, "cap_bytes", maxEgressBodyBytes,
				"total", s.oversized.Load())
			return
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))

		// netFlow here is the response direction; hand the callback the request
		// direction so src/dst mean what the caller expects.
		s.OnTransaction(netFlow.Reverse(), transportFlow.Reverse(), pending.req, resp)
	}
}

// readCappedBody reads at most maxEgressBodyBytes+1 so an over-cap body is
// detected rather than silently clipped at the limit.
func readCappedBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxEgressBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxEgressBodyBytes {
		return nil, errBodyTooLarge
	}
	return body, nil
}

func (s *StreamFactory) logRequest(netFlow, transportFlow gopacket.Flow, req *http.Request) {
	bodyBytes, _ := io.ReadAll(req.Body)
	fields := []any{
		"src", netFlow.Src().String(), "dst", netFlow.Dst().String(),
		"ports", transportFlow.String(),
		"method", req.Method, "host", req.Host, "uri", req.RequestURI,
		"body_bytes", len(bodyBytes),
	}
	if s.LogBodies {
		truncated := bodyBytes
		if len(truncated) > logBodyCap {
			truncated = truncated[:logBodyCap]
		}
		fields = append(fields, "body", string(truncated),
			"body_truncated", len(bodyBytes) > logBodyCap)
	}
	s.logger().Info("http request", fields...)
}

func (s *StreamFactory) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
