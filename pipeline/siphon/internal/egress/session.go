package egress

import (
	"fmt"
	"io"
	"sync"

	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
)

// pipeStream implements tcpassembly.Stream without blocking the assembler.
// Bytes are written to an io.Pipe; runRelay reads from the other end.
type pipeStream struct {
	pw        *io.PipeWriter
	sess      *Session
	isRequest bool
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
	if p.sess != nil {
		p.sess.markLegClosed(p.isRequest)
	}
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
	poolMgr  *forward.PoolManager
}

type Session struct {
	mu           sync.Mutex
	flowKey      string
	reqR         io.ReadCloser
	resR         io.ReadCloser
	reqS         *pipeStream
	resS         *pipeStream
	relayRunning bool
	Target       *config.SiphonTarget
}

func NewSessionStore(poolMgr *forward.PoolManager) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		poolMgr:  poolMgr,
	}
}

func allocBothPipes(sess *Session) {
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	sess.reqR = reqR
	sess.resR = resR
	sess.reqS = &pipeStream{pw: reqW, sess: sess, isRequest: true}
	sess.resS = &pipeStream{pw: resW, sess: sess, isRequest: false}
}

func (s *Session) markLegClosed(isRequest bool) {
	s.mu.Lock()
	if isRequest {
		s.reqS = nil
		s.reqR = nil
	} else {
		s.resS = nil
		s.resR = nil
	}
	s.mu.Unlock()
}

// tryStartRelay marks the session as relay-running. Caller must go runRelay when true.
func (s *Session) tryStartRelay() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relayRunning {
		return false
	}
	s.relayRunning = true
	return true
}

func (s *Session) clearRelayRunning() {
	s.mu.Lock()
	s.relayRunning = false
	s.mu.Unlock()
}

func (st *SessionStore) GetOrCreate(flowKey string, isRequest bool, target *config.SiphonTarget) tcpassembly.Stream {
	st.mu.Lock()
	defer st.mu.Unlock()

	sess, ok := st.sessions[flowKey]
	if !ok {
		sess = &Session{flowKey: flowKey, Target: target}
		allocBothPipes(sess)
		st.sessions[flowKey] = sess
		if sess.tryStartRelay() {
			go runRelay(sess, st.poolMgr)
		}
	}

	var stream tcpassembly.Stream
	var startRelay bool
	if isRequest {
		if sess.reqS == nil {
			pr, pw := io.Pipe()
			sess.reqR = pr
			sess.reqS = &pipeStream{pw: pw, sess: sess, isRequest: true}
			startRelay = sess.tryStartRelay()
		}
		stream = sess.reqS
	} else {
		if sess.resS == nil {
			pr, pw := io.Pipe()
			sess.resR = pr
			sess.resS = &pipeStream{pw: pw, sess: sess, isRequest: false}
		}
		stream = sess.resS
	}

	if startRelay {
		go runRelay(sess, st.poolMgr)
	}

	return stream
}

func (st *SessionStore) Remove(flowKey string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, flowKey)
}

func FlowKey(prodIP string, prodPort int, remoteIP string, remotePort int) string {
	return fmt.Sprintf("%s:%d-%s:%d", prodIP, prodPort, remoteIP, remotePort)
}
