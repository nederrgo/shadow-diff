package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shadow-diff/beru/internal/replay"
)

func TestSeedMock_roundTrip(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	payload := map[string]any{
		"method": "POST",
		"host":   "api.example.com",
		"path":   "/v1/orders",
		"body":   map[string]any{"amount": 100, "timestamp": "ignore"},
		"ignore_paths": []string{"$.timestamp"},
		"response": map[string]any{
			"status":  200,
			"headers": map[string]string{"content-type": "application/json"},
			"body":    `{"ok":true}`,
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/seed_mock", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.handleSeedMock(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Hash == "" {
		t.Fatal("expected hash in response")
	}
	if _, ok := mocks.Get(out.Hash); !ok {
		t.Fatal("mock not stored")
	}
}
