package egress

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
)

const truncationIdleTimeout = 500 * time.Millisecond // ponytail: tied to 250ms capture flush tick

// pipeStream accumulates TCP leg bytes until ReassemblyComplete, then relays when both legs close.
type pipeStream struct {
	sess      *Session
	store     *SessionStore
	isRequest bool
	chunks    int
}

func (p *pipeStream) Reassembled(reassemblies []tcpassembly.Reassembly) {
	for _, r := range reassemblies {
		if len(r.Bytes) == 0 {
			continue
		}
		p.chunks++
		n := len(r.Bytes)
		total := p.sess.legBytes(p.isRequest) + n
		leg := "response"
		if p.isRequest {
			leg = "request"
		}
		flow := ""
		if p.sess != nil {
			flow = p.sess.flowKey
		}
		if strings.Contains(flow, ":80") {
			log.Printf("siphon debug: egress pipe flow=%s leg=%s chunk=%d len=%d total=%d",
				flow, leg, p.chunks, n, total)
		}
		p.sess.append(p.isRequest, r.Bytes)
	}
	p.sess.tryCloseLegIfComplete(p.isRequest, p.store)
}

func (p *pipeStream) ReassemblyComplete() {
	leg := "response"
	if p.isRequest {
		leg = "request"
	}
	if p.sess == nil {
		return
	}
	if p.isRequest {
		p.sess.maybeApplyRequestTruncationFallback()
	} else {
		p.sess.finalizeResponseOnFIN()
	}
	total := p.sess.legBytes(p.isRequest)
	p.sess.tryCloseLegIfComplete(p.isRequest, p.store)
	if p.sess.legHTTPComplete(p.isRequest) || p.sess.legAlreadyClosed(p.isRequest) {
		log.Printf("siphon debug: egress leg complete flow=%s leg=%s total=%d bytes",
			p.sess.flowKey, leg, total)
		return
	}
	log.Printf("siphon debug: egress leg flush flow=%s leg=%s total=%d bytes incomplete HTTP, awaiting more PCA or idle timeout",
		p.sess.flowKey, leg, total)
}

func (s *Session) legHTTPComplete(isRequest bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isRequest {
		return httpLegComplete(s.reqBuf)
	}
	return httpLegComplete(s.resBuf)
}

func (s *Session) finalizeResponseOnFIN() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if httpLegComplete(s.resBuf) {
		return
	}
	before := len(s.resBuf)
	s.resBuf = finalizeTruncatedResponse(s.resBuf)
	if !httpLegComplete(s.resBuf) {
		return
	}
	if len(s.resBuf) != before || before == 0 {
		log.Printf("siphon debug: egress response finalized on FIN flow=%s bytes=%d (PCA-capped)",
			s.flowKey, len(s.resBuf))
	}
}

// maybeApplyRequestTruncationFallback rewrites Content-Length when PCA truncated the request body.
func (s *Session) maybeApplyRequestTruncationFallback() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !isHTTPLeg(s.reqBuf) {
		return httpLegComplete(s.reqBuf)
	}
	if httpLegComplete(s.reqBuf) {
		return true
	}
	out, applied := applyRequestTruncationFallback(s.reqBuf)
	s.reqBuf = out
	if applied {
		log.Printf("siphon debug: egress request finalized flow=%s bytes=%d (PCA-capped)",
			s.flowKey, len(s.reqBuf))
	} else if len(s.reqBuf) > 0 && !httpLegComplete(s.reqBuf) && strings.Contains(s.flowKey, ":80") {
		log.Printf("siphon debug: request truncation fallback skipped flow=%s len=%d headers_complete=%v",
			s.flowKey, len(s.reqBuf), httpHeadersComplete(s.reqBuf))
	}
	return httpLegComplete(s.reqBuf)
}

func (s *Session) legAlreadyClosed(isRequest bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isRequest {
		return s.reqClosed
	}
	return s.resClosed
}

func (s *Session) tryCloseLegIfComplete(isRequest bool, store *SessionStore) {
	if s.legAlreadyClosed(isRequest) {
		return
	}
	if isRequest && !s.legHTTPComplete(true) {
		s.mu.Lock()
		httpReq := isHTTPLeg(s.reqBuf)
		s.mu.Unlock()
		if httpReq {
			s.maybeApplyRequestTruncationFallback()
		}
	}
	if !s.legHTTPComplete(isRequest) {
		return
	}
	s.markLegClosed(isRequest)
	if s.bothLegsClosed() && s.tryStartRelay() {
		go runRelay(s, store.poolMgr)
	}
}

// ClosePipeWriter is a no-op; kept for cappedStream discard path compatibility.
func ClosePipeWriter(s tcpassembly.Stream) {}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	poolMgr  *forward.PoolManager
}

type Session struct {
	mu              sync.Mutex
	flowKey         string
	reqBuf          []byte
	resBuf          []byte
	reqClosed       bool
	resClosed       bool
	relayRunning    bool
	reqLastAppendAt time.Time
	Target          *config.SiphonTarget
}

func NewSessionStore(poolMgr *forward.PoolManager) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		poolMgr:  poolMgr,
	}
}

func (st *SessionStore) ProcessIdleTruncation() {
	st.mu.Lock()
	sessions := make([]*Session, 0, len(st.sessions))
	for _, sess := range st.sessions {
		sessions = append(sessions, sess)
	}
	st.mu.Unlock()

	now := time.Now()
	for _, sess := range sessions {
		sess.mu.Lock()
		idle := !sess.reqClosed && len(sess.reqBuf) > 0 && isHTTPLeg(sess.reqBuf) &&
			!httpLegComplete(sess.reqBuf) &&
			!sess.reqLastAppendAt.IsZero() && now.Sub(sess.reqLastAppendAt) >= truncationIdleTimeout
		sess.mu.Unlock()
		if !idle {
			continue
		}
		sess.maybeApplyRequestTruncationFallback()
		sess.tryCloseLegIfComplete(true, st)
	}
}

func (s *Session) append(isRequest bool, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isRequest {
		s.reqBuf = append(s.reqBuf, b...)
		s.reqLastAppendAt = time.Now()
	} else {
		s.resBuf = append(s.resBuf, b...)
	}
}

func (s *Session) legBytes(isRequest bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isRequest {
		return len(s.reqBuf)
	}
	return len(s.resBuf)
}

func (s *Session) snapshotPayloads() (req, res []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqBuf) > 0 {
		req = append([]byte(nil), s.reqBuf...)
	}
	if len(s.resBuf) > 0 {
		res = append([]byte(nil), s.resBuf...)
	}
	return req, res
}

func (s *Session) clearPayloads() {
	s.mu.Lock()
	s.reqBuf = nil
	s.resBuf = nil
	s.mu.Unlock()
}

func (s *Session) markLegClosed(isRequest bool) {
	s.mu.Lock()
	if isRequest {
		s.reqClosed = true
	} else {
		s.resClosed = true
	}
	s.mu.Unlock()
}

func (s *Session) bothLegsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqClosed && s.resClosed
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

func (s *Session) resetForKeepAlive() {
	s.mu.Lock()
	s.reqClosed = false
	s.resClosed = false
	s.reqBuf = nil
	s.resBuf = nil
	s.reqLastAppendAt = time.Time{}
	s.mu.Unlock()
}

func (st *SessionStore) GetOrCreate(flowKey string, isRequest bool, target *config.SiphonTarget) tcpassembly.Stream {
	st.mu.Lock()
	defer st.mu.Unlock()

	sess, ok := st.sessions[flowKey]
	if !ok {
		sess = &Session{flowKey: flowKey, Target: target}
		st.sessions[flowKey] = sess
	}

	return &pipeStream{sess: sess, store: st, isRequest: isRequest}
}

func (st *SessionStore) Remove(flowKey string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, flowKey)
}

func FlowKey(prodIP string, prodPort int, remoteIP string, remotePort int) string {
	return fmt.Sprintf("%s:%d-%s:%d", prodIP, prodPort, remoteIP, remotePort)
}
