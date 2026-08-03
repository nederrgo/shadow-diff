package server

import (
	"sync"

	"github.com/shadow-diff/tusk/pkg/db"
)

// DiffFrame is one WebSocket JSON message for /ws/diffs.
type DiffFrame struct {
	Type              string `json:"type"` // "summary" | "verdict"
	SessionID         string `json:"session_id"`
	ReplayExecutionID string `json:"replay_execution_id,omitempty"`
	TraceID           string `json:"trace_id,omitempty"`
	Verdict           string `json:"verdict,omitempty"`
	Total             int    `json:"total,omitempty"`
	Match             int    `json:"match,omitempty"`
	Mismatch          int    `json:"mismatch,omitempty"`
	Voided            int    `json:"voided,omitempty"`
}

type diffClient struct {
	ch                chan *DiffFrame
	sessionID         string
	replayExecutionID string
}

func (c *diffClient) wants(sessionID, execID string) bool {
	if c.sessionID != "" && c.sessionID != sessionID {
		return false
	}
	if c.replayExecutionID != "" && c.replayExecutionID != execID {
		return false
	}
	return true
}

func diffCacheKey(sessionID, execID string) string {
	return sessionID + "\x00" + execID
}

// DiffHub caches the latest summary per (session, execution) and fans frames
// to browsers on /ws/diffs.
type DiffHub struct {
	mu      sync.RWMutex
	latest  map[string]*DiffFrame // session\0exec → summary
	clients map[int64]*diffClient
	next    int64
}

func NewDiffHub() *DiffHub {
	return &DiffHub{
		latest:  map[string]*DiffFrame{},
		clients: map[int64]*diffClient{},
	}
}

// BroadcastSummary caches and delivers a summary frame.
func (h *DiffHub) BroadcastSummary(sum db.SessionSummary) {
	if sum.SessionID == "" {
		return
	}
	frame := &DiffFrame{
		Type:              "summary",
		SessionID:         sum.SessionID,
		ReplayExecutionID: sum.ReplayExecutionID,
		Total:             sum.Total,
		Match:             sum.Match,
		Mismatch:          sum.Mismatch,
		Voided:            sum.Voided,
	}
	h.broadcast(frame)
}

// BroadcastVerdict delivers a live verdict event (not cached).
func (h *DiffHub) BroadcastVerdict(ev db.VerdictEvent) {
	if ev.TraceID == "" {
		return
	}
	h.broadcast(&DiffFrame{
		Type:              "verdict",
		SessionID:         ev.SessionID,
		ReplayExecutionID: ev.ReplayExecutionID,
		TraceID:           ev.TraceID,
		Verdict:           ev.Verdict,
	})
}

// CacheSummary seeds the snapshot cache without fan-out (used on WS connect).
func (h *DiffHub) CacheSummary(sum db.SessionSummary) {
	if sum.SessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest[diffCacheKey(sum.SessionID, sum.ReplayExecutionID)] = &DiffFrame{
		Type:              "summary",
		SessionID:         sum.SessionID,
		ReplayExecutionID: sum.ReplayExecutionID,
		Total:             sum.Total,
		Match:             sum.Match,
		Mismatch:          sum.Mismatch,
		Voided:            sum.Voided,
	}
}

func (h *DiffHub) broadcast(frame *DiffFrame) {
	h.mu.Lock()
	if frame.Type == "summary" {
		h.latest[diffCacheKey(frame.SessionID, frame.ReplayExecutionID)] = frame
	}
	clients := make([]*diffClient, 0, len(h.clients))
	for _, c := range h.clients {
		if c.wants(frame.SessionID, frame.ReplayExecutionID) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	// ponytail: non-blocking send; slow clients drop frames. Ceiling: stale
	// summary until the next NOTIFY. Upgrade: per-client latest-value slot.
	for _, c := range clients {
		select {
		case c.ch <- frame:
		default:
		}
	}
}

// Subscribe registers a browser for one session execution (empty sessionID = all).
func (h *DiffHub) Subscribe(sessionID, execID string) (<-chan *DiffFrame, []*DiffFrame, func()) {
	c := &diffClient{
		ch:                make(chan *DiffFrame, clientBuffer),
		sessionID:         sessionID,
		replayExecutionID: execID,
	}

	h.mu.Lock()
	id := h.next
	h.next++
	h.clients[id] = c
	snapshot := make([]*DiffFrame, 0, 1)
	if sessionID != "" {
		if f, ok := h.latest[diffCacheKey(sessionID, execID)]; ok {
			snapshot = append(snapshot, f)
		}
	} else {
		for _, f := range h.latest {
			snapshot = append(snapshot, f)
		}
	}
	h.mu.Unlock()

	return c.ch, snapshot, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.clients[id]; !ok {
			return
		}
		delete(h.clients, id)
		close(c.ch)
	}
}

func (h *DiffHub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
