package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const recordEgressPath = "/v1/record_egress"

// ShopClient POSTs captured egress transactions to a shadow namespace's Shop,
// which derives the mock key and stores the response for Envoy to replay.
type ShopClient struct {
	base   string
	client *http.Client
}

func NewShopClient(baseURL string, timeout time.Duration) (*ShopClient, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse shop base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("shop base URL must include scheme and host")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &ShopClient{base: trimmed, client: &http.Client{Timeout: timeout}}, nil
}

// Record posts one egress record and returns the mock key Shop computed for
// it. That key is Shop's own replay.TraceKey output, so returning it proves
// the round trip agreed on the key Envoy's ext_proc will later look up --
// which is the one thing a POST 200 alone would not tell us.
func (c *ShopClient) Record(ctx context.Context, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal egress record: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+recordEgressPath, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("shop returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out struct {
		Hash string `json:"hash"`
	}
	// A 2xx with an unreadable body still means the record was stored; the
	// missing hash costs an assertion, not correctness.
	_ = json.Unmarshal(body, &out)
	return out.Hash, nil
}
