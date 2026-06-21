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

type pipeWriter struct {
	w *io.PipeWriter
}

func (p *pipeWriter) write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return p.w.Write(b)
}

func (p *pipeWriter) close() {
	if p.w != nil {
		_ = p.w.Close()
	}
}

type connSession struct {
	connID       uint64
	reqR         io.ReadCloser
	resR         io.ReadCloser
	reqW         *pipeWriter
	resW         *pipeWriter
	reqBuf       []byte // ponytail: buffer until response leg; io.Pipe Write blocks without reader
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
	mu          sync.Mutex
	sessions    map[uint64]*connSession
	beru        *beru.Client
	recordAndReplay []config.RecordAndReplayHost
	pairTimeout time.Duration
	maxFrame    int
	nextConnID  uint64
	stopSweeper chan struct{}
}

// NewSessionStore creates a store with a background TTL sweeper.
func NewSessionStore(client *beru.Client, recordAndReplay []config.RecordAndReplayHost, pairTimeout time.Duration, maxFrame int) *SessionStore {
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	s := &SessionStore{
		sessions:    make(map[uint64]*connSession),
		beru:        client,
		recordAndReplay: recordAndReplay,
		pairTimeout: pairTimeout,
		maxFrame:    maxFrame,
		stopSweeper: make(chan struct{}),
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

// WriteFrame delivers payload bytes for direction R or S on connID.
func (s *SessionStore) WriteFrame(connID uint64, dir byte, payload []byte) error {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok {
		s.mu.Unlock()
		return nil
	}

	if dir == DirRequest {
		if !sess.paired {
			if len(payload) > 0 {
				sess.reqBuf = append(sess.reqBuf, payload...)
				if !sess.hasReqBytes {
					sess.hasReqBytes = true
					sess.reqFirstAt = time.Now()
				}
			}
			start := sess.resAttached && sess.hasReqBytes
			s.mu.Unlock()
			if start {
				return s.attachPipesAndParser(connID)
			}
			return nil
		}
		if sess.reqW == nil {
			pr, pw := io.Pipe()
			sess.reqR = pr
			sess.reqW = &pipeWriter{w: pw}
		}
		w := sess.reqW
		s.mu.Unlock()
		if _, err := w.write(payload); err != nil {
			return err
		}
		s.maybeStartParser(connID)
		return nil
	}

	if dir == DirResponse {
		if !sess.paired {
			sess.resBuf = append(sess.resBuf, payload...)
			sess.resAttached = true
			start := sess.hasReqBytes
			s.mu.Unlock()
			if start {
				return s.attachPipesAndParser(connID)
			}
			return nil
		}
		if sess.resW == nil {
			pr, pw := io.Pipe()
			sess.resR = pr
			sess.resW = &pipeWriter{w: pw}
		}
		w := sess.resW
		s.mu.Unlock()
		if _, err := w.write(payload); err != nil {
			return err
		}
		s.maybeStartParser(connID)
		return nil
	}

	s.mu.Unlock()
	return nil
}

func (s *SessionStore) attachPipesAndParser(connID uint64) error {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok || sess.paired {
		s.mu.Unlock()
		return nil
	}
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	sess.reqR, sess.resR = reqR, resR
	sess.reqW = &pipeWriter{w: reqW}
	sess.resW = &pipeWriter{w: resW}
	sess.paired = true
	reqBuf := append([]byte(nil), sess.reqBuf...)
	resBuf := append([]byte(nil), sess.resBuf...)
	sess.reqBuf, sess.resBuf = nil, nil
	s.mu.Unlock()

	go func() {
		_, _ = reqW.Write(reqBuf)
		_ = reqW.Close()
	}()
	go func() {
		_, _ = resW.Write(resBuf)
		// resW stays open for any follow-on S frames until FinishConn
	}()

	s.maybeStartParser(connID)
	return nil
}

func (s *SessionStore) maybeStartParser(connID uint64) {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok || sess.parserCancel != nil {
		s.mu.Unlock()
		return
	}
	if sess.reqR == nil || sess.resR == nil {
		s.mu.Unlock()
		return
	}
	reqR, resR := sess.reqR, sess.resR
	ctx, cancel := context.WithCancel(context.Background())
	sess.parserCancel = cancel
	sess.parserDone = make(chan struct{})
	ds := s.recordAndReplay
	client := s.beru
	s.mu.Unlock()

	done := sess.parserDone
	go func() {
		parse.RunBidirectional(ctx, reqR, resR, ds, client)
		close(done)
		s.RemoveConn(connID)
	}()
}

// FinishConn closes pipe writers when the TCP connection ends cleanly.
func (s *SessionStore) FinishConn(connID uint64) {
	s.mu.Lock()
	sess, ok := s.sessions[connID]
	if !ok {
		s.mu.Unlock()
		return
	}
	if sess.reqW != nil {
		sess.reqW.close()
	}
	if sess.resW != nil {
		sess.resW.close()
	}
	if sess.parserCancel != nil {
		sess.parserCancel()
	}
	s.mu.Unlock()
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
	if sess.reqW != nil {
		sess.reqW.close()
	}
	if sess.resW != nil {
		sess.resW.close()
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
