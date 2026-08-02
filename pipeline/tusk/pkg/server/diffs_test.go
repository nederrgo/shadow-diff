package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shadow-diff/tusk/pkg/db"
)

type fakeStore struct {
	sessions []db.Session
	diffs    map[string][]db.SessionDiff
	summary  map[string]db.SessionSummary
	err      error
}

func (f *fakeStore) ListSessions(context.Context) ([]db.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sessions, nil
}

func (f *fakeStore) GetSessionDiffs(_ context.Context, sessionID string) ([]db.SessionDiff, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.diffs[sessionID], nil
}

func (f *fakeStore) SessionSummary(_ context.Context, sessionID string) (db.SessionSummary, error) {
	if f.err != nil {
		return db.SessionSummary{}, f.err
	}
	if s, ok := f.summary[sessionID]; ok {
		return s, nil
	}
	return db.SessionSummary{SessionID: sessionID}, nil
}

func diffTestServer(t *testing.T, store SessionStore) (*DiffHub, string) {
	t.Helper()
	hub := NewHub()
	diffHub := NewDiffHub()
	h := &HTTPServer{
		Hub:     hub,
		DiffHub: diffHub,
		Store:   store,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return diffHub, srv.URL
}

func TestDiffsAPI_NoStoreReturns503(t *testing.T) {
	_, base := diffTestServer(t, nil)

	for _, path := range []string{"/api/v1/sessions", "/api/v1/diffs?session_id=s1", "/ws/diffs?session_id=s1"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", path, resp.StatusCode)
		}
	}
}

func TestDiffsAPI_ListSessions(t *testing.T) {
	store := &fakeStore{
		sessions: []db.Session{{
			SessionID:      "session-1",
			ShadowTestName: "orders",
			Namespace:      "default",
			Mode:           "replay",
			CreatedAt:      time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		}},
	}
	_, base := diffTestServer(t, store)

	resp, err := http.Get(base + "/api/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got []db.Session
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "session-1" {
		t.Fatalf("sessions = %+v", got)
	}
}

func TestDiffsAPI_GetDiffsRequiresSessionID(t *testing.T) {
	_, base := diffTestServer(t, &fakeStore{})
	resp, err := http.Get(base + "/api/v1/diffs")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDiffsAPI_GetDiffs(t *testing.T) {
	store := &fakeStore{
		diffs: map[string][]db.SessionDiff{
			"session-1": {{
				TraceID:   "t1",
				SessionID: "session-1",
				Signature: "http:GET:/",
				Verdict:   "MATCH",
			}},
		},
	}
	_, base := diffTestServer(t, store)

	resp, err := http.Get(base + "/api/v1/diffs?session_id=session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got []db.SessionDiff
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TraceID != "t1" {
		t.Fatalf("diffs = %+v", got)
	}
}

func TestDiffHub_SnapshotAndLive(t *testing.T) {
	store := &fakeStore{
		summary: map[string]db.SessionSummary{
			"session-1": {SessionID: "session-1", Total: 3, Match: 2, Mismatch: 1},
		},
	}
	diffHub, base := diffTestServer(t, store)
	wsURL := "ws" + strings.TrimPrefix(base, "http")

	conn := dial(t, wsURL+"/ws/diffs?session_id=session-1")
	got := readDiffFrame(t, conn)
	if got.Type != "summary" || got.Total != 3 || got.Mismatch != 1 {
		t.Fatalf("snapshot = %+v", got)
	}

	waitForDiffClients(t, diffHub, 1)
	diffHub.BroadcastVerdict(db.VerdictEvent{
		SessionID: "session-1",
		TraceID:   "t-new",
		Verdict:   "MISMATCH",
	})
	live := readDiffFrame(t, conn)
	if live.Type != "verdict" || live.TraceID != "t-new" {
		t.Fatalf("live = %+v", live)
	}
}

func TestDiffHub_FiltersBySession(t *testing.T) {
	hub := NewDiffHub()
	hub.CacheSummary(db.SessionSummary{SessionID: "a", Total: 1})
	hub.CacheSummary(db.SessionSummary{SessionID: "b", Total: 2})

	ch, snap, unsub := hub.Subscribe("a")
	defer unsub()
	if len(snap) != 1 || snap[0].SessionID != "a" {
		t.Fatalf("snapshot = %+v", snap)
	}

	hub.BroadcastVerdict(db.VerdictEvent{SessionID: "b", TraceID: "x", Verdict: "MATCH"})
	select {
	case f := <-ch:
		t.Fatalf("leaked frame for other session: %+v", f)
	case <-time.After(100 * time.Millisecond):
	}

	hub.BroadcastVerdict(db.VerdictEvent{SessionID: "a", TraceID: "y", Verdict: "MATCH"})
	select {
	case f := <-ch:
		if f.TraceID != "y" {
			t.Fatalf("frame = %+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("missing frame for subscribed session")
	}
}

func readDiffFrame(t *testing.T, conn interface {
	SetReadDeadline(time.Time) error
	ReadMessage() (int, []byte, error)
}) *DiffFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f DiffFrame
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &f
}

func waitForDiffClients(t *testing.T, h *DiffHub, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.clientCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("diff client count never reached %d", want)
}
