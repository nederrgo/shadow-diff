package ingest

import (
	"context"
	"io"
	"log"
	"sync"
	"time"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
	"github.com/shadow-diff/recorder/internal/parse"
)

type connSession struct {
	connID       uint64
	reqR         io.ReadCloser
	resR         io.ReadCloser
	reqBuf       []byte
	resBuf       []byte
	paired       bool
	hasReqBytes  bool
	resAttached  bool
	reqFirstAt   time.Time
	parserCancel context.CancelFunc
	parserDone   chan struct{}
}

// SessionStore tracks in-flight pairing per Siphon TCP connection.
type SessionStore struct {
	mu              sync.Mutex
	sessions        map[uint64]*connSession
	beru            *beru.Client
	recordAndReplay []config.RecordAndReplayHost
	pairTimeout     time.Duration
	maxFrame        int
	nextConnID      uint64
	stopSweeper     chan struct{}
}

// NewSessionStore creates a store with a background TTL sweeper.
func NewSessionStore(client *beru.Client, recordAndReplay []config.RecordAndReplayHost, pairTimeout time.Duration, maxFrame int) *SessionStore {
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	s := &SessionStore{
		sessions:        make(map[uint64]*connSession),
		beru:            client,
		recordAndReplay: recordAndReplay,
		pairTimeout:     pairTimeout,
		maxFrame:        maxFrame,
		stopSweeper:     make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

// Stop shuts down the TTL sweeper.
func (s *SessionStore) Stop() {
	close(s.stopSweeper)
}

func (s *SessionStore) sweepLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopSweeper:
			return
		case <-ticker.C:
			s.evictExpired()
		}
	}
}

func (s *SessionStore) evictExpired() {
	now := time.Now()
	var expired []uint64
	s.mu.Lock()
	for id, sess := range s.sessions {
		if !sess.hasReqBytes || sess.reqFirstAt.IsZero() {
			continue
		}
		if now.Sub(sess.reqFirstAt) > s.pairTimeout && !sess.resAttached {
			expired = append(expired, id)
		}
	}
	s.mu.Unlock()
	for _, id := range expired {
		log.Printf("recorder: evicting session conn=%d (pair timeout)", id)
		s.DiscardConn(id)
	}
}

// RegisterConn allocates a session for an incoming TCP connection.
func (s *SessionStore) RegisterConn() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextConnID++
	id := s.nextConnID
	s.sessions[id] = &connSession{connID: id}
	return id
}

// WriteFrame buffers payload until FinishConn (Siphon TCP close).
func (s *SessionStore) WriteFrame(connID uint64, dir byte, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[connID]
	if !ok || sess.paired {
		return nil
	}
	switch dir {
	case DirRequest:
		sess.reqBuf = append(sess.reqBuf, payload...)
		if !sess.hasReqBytes {
			sess.hasReqBytes = true
			sess.reqFirstAt = time.Now()
		}
	case DirResponse:
		sess.resBuf = append(sess.resBuf, payload...)
		sess.resAttached = true
	}
	return nil
}

func (s *SessionStore) attachPipesAndParser(connID uint64) error {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok || sess.paired {
		s.mu.Unlock()
		return nil
	}
	if !sess.hasReqBytes || !sess.resAttached || len(sess.reqBuf) == 0 || len(sess.resBuf) == 0 {
		s.mu.Unlock()
		return nil
	}
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	sess.reqR, sess.resR = reqR, resR
	sess.paired = true
	reqBuf := append([]byte(nil), sess.reqBuf...)
	resBuf := append([]byte(nil), sess.resBuf...)
	sess.reqBuf, sess.resBuf = nil, nil
	s.mu.Unlock()

	log.Printf("recorder debug: conn=%d pairing reqBuf=%d resBuf=%d reqPreview=%q resPreview=%q",
		connID, len(reqBuf), len(resBuf),
		payloadPreview(reqBuf, 120), payloadPreview(resBuf, 120))

	go func() {
		_, _ = reqW.Write(reqBuf)
		_ = reqW.Close()
	}()
	go func() {
		_, _ = resW.Write(resBuf)
		_ = resW.Close()
	}()

	s.startParser(connID, reqR, resR)
	return nil
}

func (s *SessionStore) startParser(connID uint64, reqR, resR io.ReadCloser) {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok || sess.parserCancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess.parserCancel = cancel
	sess.parserDone = make(chan struct{})
	ds := s.recordAndReplay
	client := s.beru
	done := sess.parserDone
	s.mu.Unlock()

	go func() {
		parse.RunBidirectional(ctx, reqR, resR, ds, client)
		close(done)
		s.RemoveConn(connID)
	}()
}

// FinishConn flushes buffered frames and starts the parser when both legs arrived.
func (s *SessionStore) FinishConn(connID uint64) {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok {
		s.mu.Unlock()
		return
	}
	ready := !sess.paired && sess.hasReqBytes && sess.resAttached && len(sess.reqBuf) > 0 && len(sess.resBuf) > 0
	s.mu.Unlock()
	if ready {
		_ = s.attachPipesAndParser(connID)
		return
	}
	s.DiscardConn(connID)
}

// DiscardConn tears down incomplete pairs for a connection (EOF, reset, bad frame).
func (s *SessionStore) DiscardConn(connID uint64) {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, connID)
	if sess.parserCancel != nil {
		sess.parserCancel()
	}
	if sess.reqR != nil {
		_ = sess.reqR.Close()
	}
	if sess.resR != nil {
		_ = sess.resR.Close()
	}
	s.mu.Unlock()
}

// RemoveConn deletes a session after parser completion.
func (s *SessionStore) RemoveConn(connID uint64) {
	s.mu.Lock()
	delete(s.sessions, connID)
	s.mu.Unlock()
}

// SessionCount returns the number of active sessions (for tests).
func (s *SessionStore) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}
