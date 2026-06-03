package egress

import (
	"fmt"
	"io"
	"sync"

	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/config"
)

// pipeStream implements tcpassembly.Stream without blocking the assembler.
// Bytes are written to an io.Pipe; the parser reads from the other end via bufio.
type pipeStream struct {
	pw *io.PipeWriter
}

func (p *pipeStream) Reassembled(reassemblies []tcpassembly.Reassembly) {
	for _, r := range reassemblies {
		if len(r.Bytes) == 0 {
			continue
		}
		_, _ = p.pw.Write(r.Bytes)
	}
}

func (p *pipeStream) ReassemblyComplete() {
	_ = p.pw.Close()
}

// ClosePipeWriter closes the pipe writer for a pipeStream returned by GetOrCreate.
func ClosePipeWriter(s tcpassembly.Stream) {
	if ps, ok := s.(*pipeStream); ok && ps.pw != nil {
		_ = ps.pw.Close()
	}
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	forward  *Forwarder
}

type Session struct {
	reqR io.ReadCloser
	resR io.ReadCloser
	reqS *pipeStream
	resS *pipeStream
	// parserStarted guards the single parser goroutine per TCP connection.
	parserStarted bool
	Target        *config.SiphonTarget
}

func NewSessionStore(forward *Forwarder) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		forward:  forward,
	}
}

// GetOrCreate returns a tcpassembly.Stream for the given flow direction.
// A parser goroutine starts on the first leg so Reassembled never blocks waiting
// for a tcpreader consumer (which previously deadlocked the assembler).
func (s *SessionStore) GetOrCreate(flowKey string, isRequest bool, target *config.SiphonTarget) tcpassembly.Stream {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[flowKey]
	if !ok {
		sess = &Session{Target: target}
		s.sessions[flowKey] = sess
	}

	var stream tcpassembly.Stream
	if isRequest {
		if sess.reqS == nil {
			pr, pw := io.Pipe()
			sess.reqR = pr
			sess.reqS = &pipeStream{pw: pw}
		}
		stream = sess.reqS
	} else {
		if sess.resS == nil {
			pr, pw := io.Pipe()
			sess.resR = pr
			sess.resS = &pipeStream{pw: pw}
		}
		stream = sess.resS
	}

	if !sess.parserStarted {
		sess.parserStarted = true
		go ParseBidirectionalStream(sess, s.forward)
	}

	return stream
}

func (s *SessionStore) Remove(flowKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, flowKey)
}

func FlowKey(prodIP string, prodPort int, remoteIP string, remotePort int) string {
	return fmt.Sprintf("%s:%d-%s:%d", prodIP, prodPort, remoteIP, remotePort)
}
