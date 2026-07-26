package export

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

func ipv4Flow(src, dst string) gopacket.Flow {
	f, err := gopacket.FlowFromEndpoints(
		layers.NewIPEndpoint(net.ParseIP(src).To4()),
		layers.NewIPEndpoint(net.ParseIP(dst).To4()),
	)
	if err != nil {
		panic(err)
	}
	return f
}

func TestExporter_Handle_forwardsAdmitted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("traceparent") == "" {
			t.Error("missing traceparent")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"x":1}` {
			t.Errorf("body %q", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	router := NewRouter(slog.Default())
	router.Rebuild([]RuleExport{{
		Key: "default/r1", IPs: []string{"10.1.2.3"},
		IgrisBaseURL: srv.URL, SamplePercentage: 100,
	}})
	exp := NewExporter(router, 2, 16, slog.Default())
	defer exp.Stop()

	tp := "00-00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-00f067aa0ba902b7-01"
	req, err := http.NewRequest(http.MethodPost, "http://10.1.2.3/v1/x", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.RequestURI = "/v1/x"
	req.Host = "api.prod"
	req.Header.Set("traceparent", tp)

	exp.Handle(ipv4Flow("10.9.9.9", "10.1.2.3"), req)

	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits=%d want 1", hits.Load())
	}
}

func TestExporter_Handle_dropsUntraced(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	router := NewRouter(slog.Default())
	router.Rebuild([]RuleExport{{
		Key: "default/r1", IPs: []string{"10.1.2.3"},
		IgrisBaseURL: srv.URL, SamplePercentage: 100,
	}})
	exp := NewExporter(router, 1, 8, slog.Default())
	defer exp.Stop()

	req, _ := http.NewRequest(http.MethodGet, "http://10.1.2.3/", nil)
	req.RequestURI = "/"
	exp.Handle(ipv4Flow("10.9.9.9", "10.1.2.3"), req)
	time.Sleep(50 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("untraced should drop, hits=%d", hits.Load())
	}
}

func TestExporter_Handle_unknownDst(t *testing.T) {
	router := NewRouter(slog.Default())
	exp := NewExporter(router, 1, 8, slog.Default())
	defer exp.Stop()

	req, _ := http.NewRequest(http.MethodGet, "http://10.0.0.1/", nil)
	req.RequestURI = "/"
	req.Header.Set("traceparent", "00-00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-00f067aa0ba902b7-01")
	exp.Handle(ipv4Flow("10.9.9.9", "10.0.0.1"), req)
	// no panic / no route
}
