package beru

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Report is the payload posted to Beru's egress diff endpoint.
type Report struct {
	TraceID  string          `json:"trace_id"`
	Workload string          `json:"workload"`
	Protocol string          `json:"protocol"`
	Payload  json.RawMessage `json:"payload"`
}

// Client posts egress reports to Beru.
type Client struct {
	URL    string
	HTTP   *http.Client
}

// NewClient builds a Beru HTTP client.
func NewClient(url string) *Client {
	return &Client{
		URL: url,
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// PostReport sends one egress diff report.
func (c *Client) PostReport(ctx context.Context, report Report) error {
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
