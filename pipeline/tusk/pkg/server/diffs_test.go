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
	sessions      []db.Session
	withDiffsOnly []db.Session // returned when opts.WithDiffsOnly
	executions    map[string][]db.ReplayExecution
	diffs         map[string][]db.SessionDiff
	occurrences   map[string]db.SignatureOccurrences // key: traceID\0signature
	summary       map[string]db.SessionSummary
	latestExec    map[string]string
	err           error
	lastOpts      db.ListSessionsOpts
}

func (f *fakeStore) ListSessions(_ context.Context, opts db.ListSessionsOpts) ([]db.Session, error) {
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	if opts.WithDiffsOnly {
		if f.withDiffsOnly != nil {
			return f.withDiffsOnly, nil
		}
		return []db.Session{}, nil
	}
	return f.sessions, nil
}

func (f *fakeStore) ListExecutions(_ context.Context, sessionID string) ([]db.ReplayExecution, error) {
	if f.err != nil {
		return nil, f.err
	}
	if e, ok := f.executions[sessionID]; ok {
		return e, nil
	}
	return []db.ReplayExecution{}, nil
}

func (f *fakeStore) ResolveExecutionID(_ context.Context, sessionID, execID string) (string, error) {
	if execID != "" {
		return execID, nil
	}
	if f.latestExec != nil {
		return f.latestExec[sessionID], nil
	}
	return "exec-latest", nil
}

func (f *fakeStore) GetSessionDiffs(_ context.Context, sessionID, _ string) ([]db.SessionDiff, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.diffs[sessionID], nil
}

func (f *fakeStore) GetSignatureOccurrences(_ context.Context, traceID, signature, _ string) (db.SignatureOccurrences, error) {
	if f.err != nil {
		return db.SignatureOccurrences{}, f.err
	}
	if o, ok := f.occurrences[traceID+"\x00"+signature]; ok {
		return o, nil
	}
	return db.SignatureOccurrences{
		TraceID:     traceID,
		Signature:   signature,
		Occurrences: []db.SignatureOccurrence{},
	}, nil
}

func (f *fakeStore) SessionSummary(_ context.Context, sessionID, execID string) (db.SessionSummary, error) {
	if f.err != nil {
		return db.SessionSummary{}, f.err
	}
	key := sessionID
	if execID != "" {
		key = sessionID + "\x00" + execID
	}
	if s, ok := f.summary[key]; ok {
		return s, nil
	}
	if s, ok := f.summary[sessionID]; ok {
		if s.ReplayExecutionID == "" {
			s.ReplayExecutionID = execID
		}
		return s, nil
	}
	return db.SessionSummary{SessionID: sessionID, ReplayExecutionID: execID}, nil
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

	for _, path := range []string{
		"/api/v1/sessions",
		"/api/v1/diffs?session_id=s1",
		"/api/v1/diffs/occurrences?trace_id=t1&signature=s",
		"/ws/diffs?session_id=s1",
	} {
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
		withDiffsOnly: []db.Session{{
			SessionID:      "session-with-data",
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
	if len(got) != 1 || got[0].SessionID != "session-1" || store.lastOpts.WithDiffsOnly {
		t.Fatalf("sessions = %+v opts=%+v", got, store.lastOpts)
	}

	resp2, err := http.Get(base + "/api/v1/sessions?with_diffs=true")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	var filtered []db.Session
	if err := json.NewDecoder(resp2.Body).Decode(&filtered); err != nil {
		t.Fatal(err)
	}
	if !store.lastOpts.WithDiffsOnly || len(filtered) != 1 || filtered[0].SessionID != "session-with-data" {
		t.Fatalf("with_diffs = %+v opts=%+v", filtered, store.lastOpts)
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

func TestDiffsAPI_GetOccurrencesRequiresParams(t *testing.T) {
	_, base := diffTestServer(t, &fakeStore{})
	resp, err := http.Get(base + "/api/v1/diffs/occurrences?trace_id=t1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDiffsAPI_GetOccurrences(t *testing.T) {
	store := &fakeStore{
		occurrences: map[string]db.SignatureOccurrences{
			"t1\x00mongodb:insert:orders": {
				TraceID:   "t1",
				Signature: "mongodb:insert:orders",
				Occurrences: []db.SignatureOccurrence{{
					Index:           0,
					ControlAPayload: json.RawMessage(`{"n":1}`),
					CandidatePayload: json.RawMessage(`{"n":9}`),
				}},
			},
		},
	}
	_, base := diffTestServer(t, store)

	resp, err := http.Get(base + "/api/v1/diffs/occurrences?trace_id=t1&signature=mongodb:insert:orders")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got db.SignatureOccurrences
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TraceID != "t1" || len(got.Occurrences) != 1 || string(got.Occurrences[0].CandidatePayload) != `{"n":9}` {
		t.Fatalf("got = %+v", got)
	}
}

func TestDiffHub_SnapshotAndLive(t *testing.T) {
	store := &fakeStore{
		latestExec: map[string]string{"session-1": "exec-1"},
		summary: map[string]db.SessionSummary{
			"session-1": {
				SessionID: "session-1", ReplayExecutionID: "exec-1",
				Total: 3, Match: 2, Mismatch: 1,
			},
		},
	}
	diffHub, base := diffTestServer(t, store)
	wsURL := "ws" + strings.TrimPrefix(base, "http")

	conn := dial(t, wsURL+"/ws/diffs?session_id=session-1&replay_execution_id=exec-1")
	got := readDiffFrame(t, conn)
	if got.Type != "summary" || got.Total != 3 || got.Mismatch != 1 {
		t.Fatalf("snapshot = %+v", got)
	}

	waitForDiffClients(t, diffHub, 1)
	diffHub.BroadcastVerdict(db.VerdictEvent{
		SessionID:         "session-1",
		ReplayExecutionID: "exec-1",
		TraceID:           "t-new",
		Verdict:           "MISMATCH",
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

	ch, snap, unsub := hub.Subscribe("a", "")
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

func TestDiffsAPI_ListExecutions(t *testing.T) {
	store := &fakeStore{
		executions: map[string][]db.ReplayExecution{
			"session-1": {{
				ReplayExecutionID: "exec-2",
				SessionID:         "session-1",
				CreatedAt:         time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
			}},
		},
	}
	_, base := diffTestServer(t, store)
	resp, err := http.Get(base + "/api/v1/sessions/session-1/executions")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got []db.ReplayExecution
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ReplayExecutionID != "exec-2" {
		t.Fatalf("executions = %+v", got)
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
