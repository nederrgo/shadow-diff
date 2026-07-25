package decode

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/tcpassembly"
	"github.com/gopacket/gopacket/tcpassembly/tcpreader"
)

// StreamFactory reassembles TCP streams and parses HTTP requests out of them.
//
// OnRequest is both the test hook and the export seam: main builds an
// HTTPRecord (Method, RequestURI, Host, Body, Traceparent) and POSTs to igris.
// That type lives in internal/forwarder (cannot import
// another module's internal/).
// logBodyCap bounds how much captured body content a single log line can
// carry, so one oversized request can't flood the log with its full payload.
const logBodyCap = 4096

type StreamFactory struct {
	Log *slog.Logger
	// OnRequest is called for every successfully parsed request. When nil,
	// requests are logged instead.
	OnRequest func(netFlow, transportFlow gopacket.Flow, req *http.Request)
	// LogBodies includes body content (truncated to logBodyCap) in the
	// default log line. Only takes effect when OnRequest is nil.
	LogBodies bool
}

// New implements tcpassembly.StreamFactory.
func (s *StreamFactory) New(netFlow, transportFlow gopacket.Flow) tcpassembly.Stream {
	r := tcpreader.NewReaderStream()
	go s.run(netFlow, transportFlow, &r)
	return &r
}

func (s *StreamFactory) run(netFlow, transportFlow gopacket.Flow, r *tcpreader.ReaderStream) {
	buf := bufio.NewReader(r)
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
		// OnRequest runs BEFORE the drain so the callback can read req.Body.
		// This is the seam Step 2 uses to build HTTPRecord.Body, and it is what
		// lets a test verify a large body arrived intact.
		if s.OnRequest != nil {
			s.OnRequest(netFlow, transportFlow, req)
			// Drain whatever the callback left, so the next request on this
			// stream can be parsed.
			tcpreader.DiscardBytesToEOF(req.Body)
			req.Body.Close()
			continue
		}
		bodyBytes, _ := io.ReadAll(req.Body)
		req.Body.Close()
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
}

func (s *StreamFactory) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
