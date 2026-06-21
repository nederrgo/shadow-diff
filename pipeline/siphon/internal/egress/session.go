package egress

import (
	"fmt"
	"io"
	"log"
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
	store     *SessionStore
	isRequest bool
	logOnce   sync.Once
}

func (p *pipeStream) Reassembled(reassemblies []tcpassembly.Reassembly) {
	var wrote bool
	for _, r := range reassemblies {
		if len(r.Bytes) > 0 {
			wrote = true
			break
		}
	}
	// Start reader before Write: io.Pipe blocks until reqR/resR is read in runRelay.
	if wrote && p.store != nil && p.sess != nil && p.sess.tryStartRelay() {
		go runRelay(p.sess, p.store.poolMgr)
	}
	for _, r := range reassemblies {
		if len(r.Bytes) == 0 {
			continue
		}
		n := len(r.Bytes)
		p.logOnce.Do(func() {
			leg := "response"
			if p.isRequest {
				leg = "request"
			}
			flow := ""
			if p.sess != nil {
				flow = p.sess.flowKey
			}
			log.Printf("siphon debug: egress pipe flow=%s leg=%s first_write=%d bytes", flow, leg, n)
		})
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

func allocBothPipes(sess *Session, store *SessionStore) {
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	sess.reqR = reqR
	sess.resR = resR
	sess.reqS = &pipeStream{pw: reqW, sess: sess, store: store, isRequest: true}
	sess.resS = &pipeStream{pw: resW, sess: sess, store: store, isRequest: false}
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
		allocBothPipes(sess, st)
		st.sessions[flowKey] = sess
	}

	var stream tcpassembly.Stream
	if isRequest {
		if sess.reqS == nil {
			pr, pw := io.Pipe()
			sess.reqR = pr
			sess.reqS = &pipeStream{pw: pw, sess: sess, store: st, isRequest: true}
		}
		stream = sess.reqS
	} else {
		if sess.resS == nil {
			pr, pw := io.Pipe()
			sess.resR = pr
			sess.resS = &pipeStream{pw: pw, sess: sess, store: st, isRequest: false}
		}
		stream = sess.resS
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
