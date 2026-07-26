package forwarder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShopClientRecordPostsExpectedShape(t *testing.T) {
	var (
		gotPath        string
		gotContentType string
		gotBody        map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"hash": "trace:abc:GET:api:/x"})
	}))
	defer srv.Close()

	c, err := NewShopClient(srv.URL, time.Second)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	payload := map[string]any{
		"trace_id": "abc", "method": "GET", "host": "api", "path": "/x",
		"response": map[string]any{"status": 200, "headers": map[string]string{}, "body": "hi"},
	}
	hash, err := c.Record(context.Background(), payload)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	if gotPath != "/v1/record_egress" {
		t.Errorf("path = %q, want /v1/record_egress", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q", gotContentType)
	}
	if gotBody["trace_id"] != "abc" || gotBody["path"] != "/x" {
		t.Errorf("body = %+v", gotBody)
	}
	// The returned hash is Shop's own computed key, which is what makes a
	// round trip verifiable rather than merely successful.
	if hash != "trace:abc:GET:api:/x" {
		t.Errorf("hash = %q, want Shop's computed key", hash)
	}
}

func TestShopClientRecordSurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "trace_id is required", http.StatusBadRequest)
	}))
	defer srv.Close()

	c, err := NewShopClient(srv.URL, time.Second)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.Record(context.Background(), map[string]any{}); err == nil {
		t.Fatal("a 400 from shop must surface as an error")
	} else if !strings.Contains(err.Error(), "trace_id is required") {
		t.Errorf("error should carry shop's reason, got %v", err)
	}
}

func TestNewShopClientRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"", "shop:8080", "/v1/record_egress"} {
		if _, err := NewShopClient(bad, time.Second); err == nil {
			t.Errorf("NewShopClient(%q) should fail without scheme and host", bad)
		}
	}
}
