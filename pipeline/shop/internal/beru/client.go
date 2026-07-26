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
)

const egressDiffPath = "/api/v1/egress/diff"

// Report is the payload posted to Beru's egress diff endpoint.
type Report struct {
	TraceID        string          `json:"trace_id"`
	Workload       string          `json:"workload"`
	Protocol       string          `json:"protocol"`
	Payload        json.RawMessage `json:"payload"`
	ShadowTestName string          `json:"shadow_test_name,omitempty"`
}

// Client posts egress reports to Beru.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient builds a Beru HTTP client from a base URL (e.g. http://beru-local:8080).
func NewClient(baseURL string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		URL: base + egressDiffPath,
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// PostReport sends one egress diff report.
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
		return fmt.Errorf("beru returned status %d", resp.StatusCode)
	}
	return nil
}

// BuildHTTPEgressPayload builds the /api/v1/egress/diff payload for an HTTP egress call.
// Body is embedded JSON when valid, otherwise a JSON string (empty body → "").
func BuildHTTPEgressPayload(method, host, path string, status int, body []byte) (json.RawMessage, error) {
	var bodyJSON json.RawMessage
	switch {
	case len(body) == 0:
		bodyJSON = json.RawMessage(`""`)
	case json.Valid(body):
		bodyJSON = json.RawMessage(append([]byte(nil), body...))
	default:
		raw, err := json.Marshal(string(body))
		if err != nil {
			return nil, err
		}
		bodyJSON = raw
	}
	return json.Marshal(struct {
		Method string          `json:"method"`
		Host   string          `json:"host"`
		Path   string          `json:"path"`
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}{
		Method: method,
		Host:   host,
		Path:   path,
		Status: status,
		Body:   bodyJSON,
	})
}
