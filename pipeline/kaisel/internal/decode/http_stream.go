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
// OnRequest is both the test hook and the Step 2 seam: it is where the handler
// will build the record shape pipeline/siphon/internal/forwarder/record.go
// defines (Method, RequestURI, Host, Body, Traceparent). That type is copied
// into kaisel rather than imported -- it lives under another module's
// internal/, so importing it is not possible. pipeline/recorder does the same.
type StreamFactory struct {
	Log *slog.Logger
	// OnRequest is called for every successfully parsed request. When nil,
	// requests are logged instead.
	OnRequest func(netFlow, transportFlow gopacket.Flow, req *http.Request)
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
		body := tcpreader.DiscardBytesToEOF(req.Body)
		req.Body.Close()
		s.logger().Info("http request",
			"src", netFlow.Src().String(), "dst", netFlow.Dst().String(),
			"ports", transportFlow.String(),
			"method", req.Method, "host", req.Host, "uri", req.RequestURI,
			"body_bytes", body)
	}
}

func (s *StreamFactory) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
