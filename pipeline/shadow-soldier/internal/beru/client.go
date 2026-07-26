// Package beru reports decoded database queries to Beru's egress diff endpoint.
package beru

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shadow-diff/shadow-soldier/internal/parsers"
)

const egressDiffPath = "/api/v1/egress/diff"

// Report is the body posted to Beru.
//
// Signature is supplied rather than left to Beru's own derivation: Beru infers a
// database signature from the first non-`$` string key of the payload in sorted
// order, which is stable only as long as no other top-level string field sorts
// ahead of the operation. Raw SQL in the payload breaks that, and an unstable
// signature is the one failure that silently invalidates a whole diff.
type Report struct {
	TraceID        string          `json:"trace_id"`
	Workload       string          `json:"workload"` // shadow role
	Protocol       string          `json:"protocol"`
	Signature      string          `json:"signature,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	ShadowTestName string          `json:"shadow_test_name,omitempty"`
}

// Client posts egress reports to Beru.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient builds a Beru client from a base URL (e.g. http://beru-local:8081).
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		URL:  base + egressDiffPath,
		HTTP: &http.Client{Timeout: timeout},
	}
}

// PostReport sends one report. A non-2xx response is returned as an error along
// with its status code so the caller can decide whether it is worth retrying.
func (c *Client) PostReport(ctx context.Context, report Report) error {
	if c == nil || c.URL == "" {
		return nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("post report: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{Code: resp.StatusCode}
	}
	return nil
}

type statusError struct{ Code int }

func (e *statusError) Error() string { return fmt.Sprintf("beru returned status %d", e.Code) }

// BuildPayload renders a decoded query as the JSON body Beru stores and diffs.
//
// For MongoDB the command document is passed through as-is so its top-level keys
// survive — that is what lets Beru's mongoPayloadsEqual strip the per-connection
// noise (_id, lsid, comment, $db) before comparing. Every other protocol gets a
// small structured object.
func BuildPayload(q parsers.QueryReport, shadowPod string) (json.RawMessage, error) {
	if q.Protocol == parsers.ProtocolMongoDB && json.Valid([]byte(q.RawQuery)) {
		return json.RawMessage(q.RawQuery), nil
	}
	return json.Marshal(struct {
		Operation  string   `json:"operation"`
		Target     string   `json:"target"`
		RawQuery   string   `json:"raw_query"`
		Parameters []string `json:"parameters,omitempty"`
		ShadowPod  string   `json:"shadow_pod,omitempty"`
	}{
		Operation:  q.Operation,
		Target:     q.Target,
		RawQuery:   q.RawQuery,
		Parameters: q.Params,
		ShadowPod:  shadowPod,
	})
}
