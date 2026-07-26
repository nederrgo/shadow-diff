// Command egress-test-app is the prod-side workload for Shadow-Diff's HTTP
// egress capture tests.
//
// One binary plays both roles so a test needs a single image:
//
//	/egress/*  — the CALLER. Handles an inbound request and makes outbound HTTP
//	             calls to a dependency, propagating the inbound trace context.
//	             This is what Kaisel captures as egress.
//	/dep/*     — the DEPENDENCY. Serves deterministic responses the caller
//	             reaches out to, with the status, headers and body a test asks
//	             for. This is what gets recorded as a replayable mock.
//
// Trace context propagation is the load-bearing behaviour: Kaisel keys every
// egress mock on the trace id, so an outbound call that loses the traceparent
// is dropped before it is ever recorded.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tracingHeaders are copied from the inbound request onto every outbound call.
// Without traceparent the egress record cannot be keyed and is dropped.
var tracingHeaders = []string{"traceparent", "tracestate", "baggage"}

type callResult struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	Status     int    `json:"status"`
	BodyBytes  int    `json:"body_bytes"`
	Error      string `json:"error,omitempty"`
	ContentTyp string `json:"content_type,omitempty"`
}

type egressResponse struct {
	Traceparent string       `json:"traceparent"`
	Calls       []callResult `json:"calls"`
}

// Scenario header values for GET /egress/run. Kaisel E2E picks one per test.
const (
	scenarioLargeBody = "large-body"
	scenarioExternal  = "external"
	scenarioInCluster = "in-cluster"
	scenarioParallel  = "parallel"

	scenarioHeader = "X-Egress-Scenario"

	largeBodyBytes = 512000
)

func main() {
	addr := envOr("LISTEN_ADDR", ":8080")
	depBase := strings.TrimSuffix(envOr("DEPENDENCY_BASE_URL", "http://localhost:8080"), "/")
	depCluster := strings.TrimSuffix(envOr("DEPENDENCY_CLUSTER_URL", depBase), "/")
	externalURL := envOr("EXTERNAL_BASE_URL", "http://httpbin.org/get")

	client := &http.Client{Timeout: 15 * time.Second}
	cfg := egressConfig{
		depBase:     depBase,
		depCluster:  depCluster,
		externalURL: externalURL,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// ── caller side ────────────────────────────────────────────────────────
	mux.HandleFunc("GET /egress/get", handleEgress(client, depBase, http.MethodGet))
	mux.HandleFunc("POST /egress/post", handleEgress(client, depBase, http.MethodPost))
	mux.HandleFunc("GET /egress/head", handleEgress(client, depBase, http.MethodHead))
	mux.HandleFunc("GET /egress/multi", handleEgressMulti(client, depBase))
	mux.HandleFunc("GET /egress/run", handleEgressRun(client, cfg))

	// ── dependency side ────────────────────────────────────────────────────
	mux.HandleFunc("/dep/echo", handleDepEcho)
	mux.HandleFunc("/dep/size/{n}", handleDepSize)
	mux.HandleFunc("/dep/status/{code}", handleDepStatus)

	log.Printf("egress-test-app listening on %s (dependency=%s cluster=%s external=%s)",
		addr, depBase, depCluster, externalURL)
	srv := &http.Server{
		Addr:              addr,
		Handler:           accessLog(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

type egressConfig struct {
	depBase     string
	depCluster  string
	externalURL string
}

// accessLog prints one line per inbound request. Shadow-stack assertions grep
// the app container's log to prove igris actually delivered a replayed request
// to all three roles, so this is load-bearing for the E2E, not just debugging.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("inbound %s %s traceparent=%q", r.Method, r.URL.RequestURI(), r.Header.Get("traceparent"))
		next.ServeHTTP(w, r)
	})
}

// handleEgress makes one outbound call. The dependency path comes from the
// "path" query parameter so a test can exercise query strings, which are part
// of the mock key that Envoy's :path later looks up.
func handleEgress(client *http.Client, depBase, method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/dep/echo"
		}
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"from":"egress-test-app"}`)
		}
		res := call(client, r, method, depBase+path, body)
		writeJSON(w, egressResponse{
			Traceparent: r.Header.Get("traceparent"),
			Calls:       []callResult{res},
		})
	}
}

// handleEgressMulti makes several outbound calls under one inbound trace, so a
// test can assert that distinct dependency calls each produce their own mock
// rather than collapsing onto one key.
func handleEgressMulti(client *http.Client, depBase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.URL.Query().Get("n"))
		if err != nil || n <= 0 || n > 10 {
			n = 3
		}
		calls := make([]callResult, 0, n)
		for i := 0; i < n; i++ {
			u := fmt.Sprintf("%s/dep/echo?call=%d", depBase, i)
			calls = append(calls, call(client, r, http.MethodGet, u, nil))
		}
		writeJSON(w, egressResponse{
			Traceparent: r.Header.Get("traceparent"),
			Calls:       calls,
		})
	}
}

// handleEgressRun picks an outbound behaviour from X-Egress-Scenario so each
// Kaisel E2E test drives one capture property with a single inbound curl.
func handleEgressRun(client *http.Client, cfg egressConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scenario := strings.TrimSpace(r.Header.Get(scenarioHeader))
		calls, err := runScenario(client, r, cfg, scenario)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, egressResponse{
			Traceparent: r.Header.Get("traceparent"),
			Calls:       calls,
		})
	}
}

func runScenario(client *http.Client, inbound *http.Request, cfg egressConfig, scenario string) ([]callResult, error) {
	switch scenario {
	case scenarioLargeBody:
		return []callResult{call(client, inbound, http.MethodGet,
			fmt.Sprintf("%s/dep/size/%d", cfg.depBase, largeBodyBytes), nil)}, nil
	case scenarioExternal:
		return []callResult{call(client, inbound, http.MethodGet, cfg.externalURL, nil)}, nil
	case scenarioInCluster:
		path := inbound.URL.Query().Get("path")
		if path == "" {
			path = "/dep/echo"
		}
		return []callResult{call(client, inbound, http.MethodGet,
			cfg.depCluster+path, nil)}, nil
	case scenarioParallel:
		paths := []string{"/dep/echo?route=a", "/dep/echo?route=b", "/dep/status/200"}
		return callParallel(client, inbound, cfg.depBase, paths), nil
	case "":
		return nil, fmt.Errorf("missing %s header", scenarioHeader)
	default:
		return nil, fmt.Errorf("unknown %s %q", scenarioHeader, scenario)
	}
}

// callParallel fires concurrent GETs to distinct paths on the same host so
// Kaisel pairing can be asserted under one inbound trace.
func callParallel(client *http.Client, inbound *http.Request, depBase string, paths []string) []callResult {
	calls := make([]callResult, len(paths))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			calls[i] = call(client, inbound, http.MethodGet, depBase+p, nil)
		}(i, p)
	}
	wg.Wait()
	return calls
}

// egressRetryBudget matches the hybrid workers: shadows may dial before Kaisel
// has seeded Shop; Envoy returns 599 (miss) or sometimes 500 until the mock lands.
const egressRetryBudget = 60 * time.Second

func call(client *http.Client, inbound *http.Request, method, url string, body io.Reader) callResult {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			res := callResult{Method: method, URL: url, Error: err.Error()}
			logEgress(res)
			return res
		}
	}

	deadline := time.Now().Add(egressRetryBudget)
	var res callResult
	for {
		var rdr io.Reader
		if bodyBytes != nil {
			rdr = bytes.NewReader(bodyBytes)
		}
		res = callOnce(client, inbound, method, url, rdr)
		if res.Error != "" || (res.Status != 599 && res.Status != 500) || time.Now().After(deadline) {
			logEgress(res)
			return res
		}
		time.Sleep(2 * time.Second)
	}
}

func callOnce(client *http.Client, inbound *http.Request, method, url string, body io.Reader) callResult {
	res := callResult{Method: method, URL: url}

	req, err := http.NewRequestWithContext(inbound.Context(), method, url, body)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	// The whole point: without this the egress record has no trace id and is
	// dropped before Shop ever sees it.
	for _, h := range tracingHeaders {
		if v := inbound.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()

	read, _ := io.Copy(io.Discard, resp.Body)
	res.Status = resp.StatusCode
	res.BodyBytes = int(read)
	res.ContentTyp = resp.Header.Get("Content-Type")
	return res
}

// logEgress is greppable by bats on shadow pods. status=200 after a Shop seed
// is the Envoy→Shop mock hit (miss is 599). SHADOW_ROLE lives on the Envoy
// sidecar today, not the app, so we do not label via=replay here.
func logEgress(res callResult) {
	if res.Error != "" {
		log.Printf("http egress error=%s url=%s", res.Error, res.URL)
		return
	}
	log.Printf("http egress status=%d url=%s", res.Status, res.URL)
}

// handleDepEcho returns a deterministic JSON body plus the headers the export
// allowlist keeps (Content-Type, X-*) and ones it must drop (Date, Server are
// added by net/http itself).
func handleDepEcho(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Dependency", "egress-test-app")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"dependency": "ok",
		"path":       r.URL.RequestURI(),
	})
}

// handleDepSize returns exactly n bytes, for asserting a mock body survives
// reassembly across many packets. A truncated mock is worse than none.
func handleDepSize(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n > 8<<20 {
		http.Error(w, "bad size", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(n))
	w.WriteHeader(http.StatusOK)
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = 'z'
	}
	for written := 0; written < n; {
		chunk := len(buf)
		if r := n - written; r < chunk {
			chunk = r
		}
		m, err := w.Write(buf[:chunk])
		if err != nil {
			return
		}
		written += m
	}
}

// handleDepStatus returns the requested status code, so a test can check that
// a non-2xx dependency response is recorded as itself rather than normalized.
func handleDepStatus(w http.ResponseWriter, r *http.Request) {
	code, err := strconv.Atoi(r.PathValue("code"))
	if err != nil || code < 100 || code > 599 {
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]int{"status": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
