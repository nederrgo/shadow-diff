package http

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shadow-diff/igris/internal/payload"
)

const outboundTimeout = 5 * time.Second

type message struct {
	method     string
	requestURI string
	headers    http.Header
	body       []byte
	client     *http.Client
}

func (m *message) Dispatch(ctx context.Context, targets []payload.Target) []payload.Result {
	if m.client == nil {
		m.client = &http.Client{}
	}
	results := make([]payload.Result, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))
	for i, target := range targets {
		go func(i int, target payload.Target) {
			defer wg.Done()
			results[i] = m.sendOne(target)
		}(i, target)
	}
	wg.Wait()
	return results
}

func (m *message) sendOne(target payload.Target) payload.Result {
	destURL := strings.TrimSuffix(target.BaseURL, "/") + m.requestURI

	ctx, cancel := context.WithTimeout(context.Background(), outboundTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, m.method, destURL, bytes.NewReader(m.body))
	if err != nil {
		return payload.Result{Name: target.Name, Err: err}
	}
	req.Header = m.headers.Clone()
	req.Close = true

	resp, err := m.client.Do(req)
	if err != nil {
		return payload.Result{Name: target.Name, Err: err}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return payload.Result{Name: target.Name, StatusCode: resp.StatusCode}
}
