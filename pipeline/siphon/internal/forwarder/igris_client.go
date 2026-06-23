package forwarder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	headerShadowTraceID = "x-shadow-trace-id"
	headerTraceparent   = "traceparent"
)

// Client POSTs parsed HTTP records to igris-http.
type Client struct {
	base   *url.URL
	client *http.Client
	log    *slog.Logger
}

func NewClient(baseURL string, timeout time.Duration, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse SIPHON_IGRIS_BASE_URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("SIPHON_IGRIS_BASE_URL must include scheme and host")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		base: u,
		client: &http.Client{
			Timeout: timeout,
		},
		log: log,
	}, nil
}

func resolveIgrisURL(base *url.URL, requestURI string) (string, error) {
	requestURI = strings.TrimSpace(requestURI)
	if requestURI == "" {
		requestURI = "/"
	}

	ref, err := url.ParseRequestURI(requestURI)
	if err != nil {
		ref = &url.URL{Path: requestURI}
	}
	return base.ResolveReference(ref).String(), nil
}

func (c *Client) Forward(ctx context.Context, record HTTPRecord) error {
	target, err := resolveIgrisURL(c.base, record.RequestURI)
	if err != nil {
		return err
	}

	method := strings.TrimSpace(record.Method)
	if method == "" {
		method = http.MethodPost
	}

	var body io.Reader
	if len(record.Body) > 0 {
		body = bytes.NewReader(record.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if host := strings.TrimSpace(record.Host); host != "" {
		req.Host = host
		req.Header.Set("Host", host)
	}
	if id := strings.TrimSpace(record.ShadowTraceID); id != "" {
		req.Header.Set(headerShadowTraceID, id)
	}
	if tp := strings.TrimSpace(record.Traceparent); tp != "" {
		req.Header.Set(headerTraceparent, tp)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("igris returned %s", resp.Status)
	}
	return nil
}
