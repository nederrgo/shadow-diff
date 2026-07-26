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

func TestClient_Forward_postsWithTraceparent(t *testing.T) {
	var gotMethod, gotPath, gotTP, gotHost, gotScenario string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.RequestURI()
		gotTP = r.Header.Get(headerTraceparent)
		gotHost = r.Host
		gotScenario = r.Header.Get("X-Egress-Scenario")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	hdr := http.Header{}
	hdr.Set("X-Egress-Scenario", "in-cluster")
	hdr.Set("Connection", "keep-alive") // hop-by-hop: must not forward
	hdr.Set("Content-Type", "application/json")
	err = client.Forward(context.Background(), HTTPRecord{
		Method:      http.MethodPost,
		RequestURI:  "/echo?active=true",
		Host:        "prod.example.com",
		Body:        []byte(`{"ok":true}`),
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Headers:     hdr,
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
	if gotTP != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Fatalf("traceparent %q", gotTP)
	}
	if gotHost != "prod.example.com" {
		t.Fatalf("host %q", gotHost)
	}
	if gotScenario != "in-cluster" {
		t.Fatalf("X-Egress-Scenario %q, want in-cluster", gotScenario)
	}
	if string(gotBody) != `{"ok":true}` {
		t.Fatalf("body %q", gotBody)
	}
}

func TestCloneRequestHeadersDropsHopByHop(t *testing.T) {
	in := http.Header{}
	in.Set("X-Keep", "yes")
	in.Set("Connection", "close")
	in.Set("Host", "should-drop")
	in.Add("X-Multi", "a")
	in.Add("X-Multi", "b")
	got := CloneRequestHeaders(in)
	if got.Get("X-Keep") != "yes" {
		t.Fatalf("X-Keep=%q", got.Get("X-Keep"))
	}
	if got.Get("Connection") != "" || got.Get("Host") != "" {
		t.Fatalf("hop-by-hop leaked: %v", got)
	}
	if len(got.Values("X-Multi")) != 2 {
		t.Fatalf("X-Multi=%v", got.Values("X-Multi"))
	}
}

func TestNewClient_requiresBaseURL(t *testing.T) {
	if _, err := NewClient("", time.Second); err == nil {
		t.Fatal("expected error for empty base URL")
	}
}
