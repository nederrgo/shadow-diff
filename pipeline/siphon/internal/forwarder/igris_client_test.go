package forwarder

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestResolveIgrisURL_preservesQueryString(t *testing.T) {
	base, err := url.Parse("http://igris.shadow-ns.svc.cluster.local:8080")
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveIgrisURL(base, "/v1/users?active=true")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "%3F") {
		t.Fatalf("query encoded incorrectly: %q", got)
	}
	want := "http://igris.shadow-ns.svc.cluster.local:8080/v1/users?active=true"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestClient_Forward_postsWithTraceHeaders(t *testing.T) {
	var gotMethod, gotPath, gotTrace, gotTP string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.RequestURI()
		gotTrace = r.Header.Get(headerShadowTraceID)
		gotTP = r.Header.Get(headerTraceparent)
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = client.Forward(context.Background(), HTTPRecord{
		Method:        http.MethodPost,
		RequestURI:    "/echo?active=true",
		Body:          []byte(`{"ok":true}`),
		ShadowTraceID: "abc123",
		Traceparent:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method %q", gotMethod)
	}
	if gotPath != "/echo?active=true" {
		t.Fatalf("path %q", gotPath)
	}
	if gotTrace != "abc123" {
		t.Fatalf("trace %q", gotTrace)
	}
	if gotTP == "" {
		t.Fatal("missing traceparent")
	}
	if string(gotBody) != `{"ok":true}` {
		t.Fatalf("body %q", gotBody)
	}
}

func TestNewClient_requiresBaseURL(t *testing.T) {
	if _, err := NewClient("", time.Second, nil); err == nil {
		t.Fatal("expected error for empty base URL")
	}
}
