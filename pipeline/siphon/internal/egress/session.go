package egress

import (
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
)

// pipeStream implements tcpassembly.Stream without blocking the assembler.
type pipeStream struct {
	pw        *io.PipeWriter
	store     *SessionStore
	flowKey   string
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
	if p.store != nil {
		p.store.onLegComplete(p.flowKey, p.isRequest)
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
	flowKey      string
	reqR         io.ReadCloser
	resR         io.ReadCloser
	reqS         *pipeStream
	resS         *pipeStream
	relayStarted bool
	reqComplete  bool
	resComplete  bool
	Target       *config.SiphonTarget
	conn         net.Conn
}

func (s *Session) setConn(c net.Conn) {
	s.conn = c
}

func NewSessionStore(poolMgr *forward.PoolManager) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		poolMgr:  poolMgr,
	}
}

func (st *SessionStore) GetOrCreate(flowKey string, isRequest bool, target *config.SiphonTarget) tcpassembly.Stream {
	st.mu.Lock()
	defer st.mu.Unlock()

	sess, ok := st.sessions[flowKey]
	if !ok {
		sess = &Session{flowKey: flowKey, Target: target}
		st.sessions[flowKey] = sess
	}

	var stream tcpassembly.Stream
	if isRequest {
		if sess.reqS == nil {
			pr, pw := io.Pipe()
			sess.reqR = pr
			sess.reqS = &pipeStream{pw: pw, store: st, flowKey: flowKey, isRequest: true}
		}
		stream = sess.reqS
	} else {
		if sess.resS == nil {
			pr, pw := io.Pipe()
			sess.resR = pr
			sess.resS = &pipeStream{pw: pw, store: st, flowKey: flowKey, isRequest: false}
		}
		stream = sess.resS
	}

	if !sess.relayStarted {
		sess.relayStarted = true
		go runRelay(sess, st.poolMgr)
	}

	return stream
}

func (st *SessionStore) Remove(flowKey string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	sess, ok := st.sessions[flowKey]
	if !ok {
		return
	}
	delete(st.sessions, flowKey)
	if sess.conn != nil {
		_ = sess.conn.Close()
	}
}

func (st *SessionStore) onLegComplete(flowKey string, isRequest bool) {
	st.mu.Lock()
	sess, ok := st.sessions[flowKey]
	if !ok {
		st.mu.Unlock()
		return
	}
	if isRequest {
		sess.reqComplete = true
	} else {
		sess.resComplete = true
	}
	done := sess.reqComplete && sess.resComplete
	st.mu.Unlock()
	if done {
		st.Remove(flowKey)
	}
}

func FlowKey(prodIP string, prodPort int, remoteIP string, remotePort int) string {
	return fmt.Sprintf("%s:%d-%s:%d", prodIP, prodPort, remoteIP, remotePort)
}
