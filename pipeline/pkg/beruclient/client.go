// Package beruclient posts egress reports to Beru.
package beruclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	EgressDiffPath    = "/api/v1/egress/diff"
	DefaultTimeout    = 5 * time.Second
	DefaultMaxRetries = 3
	baseBackoff       = 50 * time.Millisecond
)

// Report is the body posted to Beru's egress diff endpoint. Signature and
// ShadowTestName are optional because not every protocol supplies them.
type Report struct {
	TraceID        string          `json:"trace_id"`
	Workload       string          `json:"workload"`
	Protocol       string          `json:"protocol"`
	Signature      string          `json:"signature,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	ShadowTestName string          `json:"shadow_test_name,omitempty"`
}

// Client posts egress reports to Beru.
type Client struct {
	URL        string
	HTTP       *http.Client
	MaxRetries int
}

// NewClient builds a client from a Beru base URL. When supplied, timeout
// overrides DefaultTimeout.
func NewClient(baseURL string, timeout ...time.Duration) *Client {
	d := DefaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		d = timeout[0]
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		URL:        base + EgressDiffPath,
		HTTP:       &http.Client{Timeout: d},
		MaxRetries: DefaultMaxRetries,
	}
}

// PostReport sends one report, retrying only failures that indicate the request
// did not reach Beru or a 5xx response.
func (c *Client) PostReport(ctx context.Context, report Report) error {
	if c == nil || c.URL == "" {
		return nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	maxRetries := max(c.MaxRetries, 0)
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(baseBackoff << (attempt - 1))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := c.postReport(ctx, raw); err != nil {
			lastErr = err
			if Retryable(err) {
				continue
			}
			return err
		}
		return nil
	}
	return lastErr
}

func (c *Client) postReport(ctx context.Context, raw []byte) error {
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
		return &StatusError{Code: resp.StatusCode}
	}
	return nil
}

// StatusError reports a non-2xx response from Beru.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("beru returned status %d", e.Code) }

// Retryable reports whether err is worth another attempt.
//
// Beru does not deduplicate: AppendReport is a plain INSERT with no idempotency
// key, so a report delivered twice becomes two rows and reads as an extra egress
// call. Retries are therefore limited to failures where the request provably
// did not reach the handler, plus explicit 5xx responses.
//
// A timeout is deliberately not retried. It cannot be distinguished from Beru
// accepting the report and replying slowly, and a duplicate is worse than a
// missing report.
func Retryable(err error) bool {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}
