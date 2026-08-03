package replay

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
	"github.com/shadow-diff/trace"
)

const outboundTimeout = 5 * time.Second

const headerShadowRole = "x-shadow-role"

// replayRetryWaits are sleeps before retry attempts 2..4 after a dial/transport error.
var replayRetryWaits = []time.Duration{1 * time.Second, 3 * time.Second, 5 * time.Second}

// sleep is overridable in tests (ponytail: real wall-clock backoff in production).
var sleep = time.Sleep

func dispatchRecord(client *http.Client, log *slog.Logger, rec driver.IngressCapture, targets []payload.Target) {
	if client == nil {
		client = &http.Client{}
	}
	if log == nil {
		log = slog.Default()
	}
	uri := rec.RequestURI
	if uri == "" {
		uri = rec.Path
	}
	baseHeaders := http.Header{}
	for k, v := range rec.Headers {
		baseHeaders.Set(k, v)
	}
	if tp := strings.TrimSpace(rec.Traceparent); tp != "" {
		baseHeaders.Set(trace.HeaderTraceparent, tp)
	}

	var wg sync.WaitGroup
	wg.Add(len(targets))
	for _, target := range targets {
		go func(target payload.Target) {
			defer wg.Done()
			res := sendOneWithRetry(client, log, rec.Method, uri, baseHeaders, rec.Body, target)
			if res.Err != nil {
				log.Info("replay delivery failed",
					"target", res.Name,
					"method", rec.Method,
					"uri", uri,
					"err", res.Err,
				)
				return
			}
			log.Info("replay delivery ok",
				"target", res.Name,
				"method", rec.Method,
				"uri", uri,
				"status", res.StatusCode,
			)
		}(target)
	}
	wg.Wait()
}

// sendOneWithRetry dials once, then up to 3 retries after 1s/3s/5s on transport errors only.
func sendOneWithRetry(
	client *http.Client,
	log *slog.Logger,
	method, requestURI string,
	headers http.Header,
	body []byte,
	target payload.Target,
) payload.Result {
	var last payload.Result
	for attempt := 0; attempt <= len(replayRetryWaits); attempt++ {
		if attempt > 0 {
			wait := replayRetryWaits[attempt-1]
			if log != nil {
				log.Info("replay delivery retry",
					"target", target.Name,
					"attempt", attempt+1,
					"wait", wait.String(),
					"err", last.Err,
				)
			}
			sleep(wait)
		}
		last = sendOne(client, method, requestURI, headers, body, target)
		if last.Err == nil {
			return last
		}
	}
	return last
}

func sendOne(client *http.Client, method, requestURI string, headers http.Header, body []byte, target payload.Target) payload.Result {
	destURL := strings.TrimSuffix(target.BaseURL, "/") + requestURI
	ctx, cancel := context.WithTimeout(context.Background(), outboundTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, destURL, bytes.NewReader(body))
	if err != nil {
		return payload.Result{Name: target.Name, Err: err}
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	req.Header.Set(headerShadowRole, target.Name)
	req.Close = true

	resp, err := client.Do(req)
	if err != nil {
		return payload.Result{Name: target.Name, Err: err}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return payload.Result{Name: target.Name, StatusCode: resp.StatusCode}
}
