package forward

import (
	"net/http"
	"testing"

	"github.com/shadow-diff/siphon/internal/config"
)

func TestStripHopByHop(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Connection", "close, X-Foo")
	req.Header.Set("X-Foo", "bar")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Accept", "application/json")

	StripHopByHop(req)

	if req.Header.Get("Connection") != "" {
		t.Fatal("Connection should be stripped")
	}
	if req.Header.Get("Keep-Alive") != "" {
		t.Fatal("Keep-Alive should be stripped")
	}
	if req.Header.Get("Transfer-Encoding") != "" {
		t.Fatal("Transfer-Encoding should be stripped")
	}
	if req.Header.Get("X-Foo") != "" {
		t.Fatal("Connection-listed header should be stripped")
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Fatal("application header should remain")
	}
}

func TestBuildOutboundRequestNoRequestURI(t *testing.T) {
	captured, _ := http.NewRequest(http.MethodGet, "http://ignored/", nil)
	captured.RequestURI = "/hello?x=1"
	captured.Host = "prod"

	out, err := buildOutboundRequest(captured, []byte("body"), config.Route{
		Target: config.Target{
			IgrisHost: "igris.shadow.svc.cluster.local",
		},
		IgrisPort:    8080,
		ShadowTestID: "default/st",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.RequestURI != "" {
		t.Fatalf("RequestURI must be empty, got %q", out.RequestURI)
	}
	if out.URL.Host != "igris.shadow.svc.cluster.local:8080" {
		t.Fatalf("URL.Host = %q", out.URL.Host)
	}
	if out.URL.Scheme != "http" {
		t.Fatalf("URL.Scheme = %q", out.URL.Scheme)
	}
	if out.Header.Get("Transfer-Encoding") != "" {
		t.Fatal("hop-by-hop should be stripped")
	}
	if out.ContentLength != 4 {
		t.Fatalf("ContentLength = %d", out.ContentLength)
	}
}
