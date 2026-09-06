// Deterministic HTTP load generator for the stress suite.
//
// Each request gets a W3C traceparent derived from SHA256(run_id|seq) so S3 and
// Postgres verifiers can cross-check every sent ID.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type sentTrace struct {
	Seq      int    `json:"seq"`
	StressID string `json:"stress_id"`
	TraceID  string `json:"trace_id"`
	OrderID  string `json:"order_id"`
}

type referenceFile struct {
	RunID  string      `json:"run_id"`
	N      int         `json:"n"`
	Traces []sentTrace `json:"traces"`
}

func main() {
	url := flag.String("url", "", "target URL (required)")
	n := flag.Int("n", 1000, "total requests")
	rpsStart := flag.Float64("rps-start", 500, "starting RPS")
	rpsEnd := flag.Float64("rps-end", 2000, "ending / sustained RPS")
	rampSec := flag.Float64("ramp-sec", 60, "seconds to ramp start→end RPS")
	runID := flag.String("run-id", "", "run UUID (auto if empty)")
	out := flag.String("out", "stress_sent_traces.json", "reference JSON path")
	timeoutSec := flag.Float64("timeout", 30, "per-request timeout seconds")
	concurrency := flag.Int("concurrency", 64, "max in-flight requests")
	flag.Parse()

	if strings.TrimSpace(*url) == "" {
		fmt.Fprintln(os.Stderr, "load_gen: -url is required")
		os.Exit(2)
	}
	if *n < 1 {
		fmt.Fprintln(os.Stderr, "load_gen: -n must be >= 1")
		os.Exit(2)
	}
	if *rpsStart <= 0 || *rpsEnd <= 0 {
		fmt.Fprintln(os.Stderr, "load_gen: RPS must be > 0")
		os.Exit(2)
	}
	if *concurrency < 1 {
		*concurrency = 1
	}

	id := strings.TrimSpace(*runID)
	if id == "" {
		id = newRunID()
	}

	client := &http.Client{
		Timeout: time.Duration(*timeoutSec * float64(time.Second)),
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	traces := make([]sentTrace, *n)
	var okCount, failCount atomic.Int64
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup

	start := time.Now()
	fmt.Fprintf(os.Stderr, "load_gen: run_id=%s n=%d rps=%g→%g ramp=%gs url=%s\n",
		id, *n, *rpsStart, *rpsEnd, *rampSec, *url)

	for seq := 1; seq <= *n; seq++ {
		waitUntilSlot(start, seq, *rpsStart, *rpsEnd, *rampSec)

		traceID := deterministicTraceID(id, seq)
		spanID := deterministicSpanID(seq)
		stressID := fmt.Sprintf("stress-run-%s-%d", id, seq)
		orderID := fmt.Sprintf("stress-%s-%d", id, seq)
		traces[seq-1] = sentTrace{
			Seq: seq, StressID: stressID, TraceID: traceID, OrderID: orderID,
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(seq int, traceID, spanID, stressID, orderID string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := sendOne(client, *url, traceID, spanID, stressID, orderID, seq); err != nil {
				failCount.Add(1)
				fmt.Fprintf(os.Stderr, "load_gen: seq=%d FAIL: %v\n", seq, err)
				return
			}
			okCount.Add(1)
		}(seq, traceID, spanID, stressID, orderID)
	}
	wg.Wait()

	ref := referenceFile{RunID: id, N: *n, Traces: traces}
	data, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load_gen: marshal reference: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "load_gen: write %s: %v\n", *out, err)
		os.Exit(1)
	}

	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "load_gen: done ok=%d fail=%d elapsed=%s out=%s\n",
		okCount.Load(), failCount.Load(), elapsed.Round(time.Millisecond), *out)
	if failCount.Load() > 0 {
		os.Exit(1)
	}
}

func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func deterministicTraceID(runID string, seq int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", runID, seq)))
	return hex.EncodeToString(sum[:16])
}

func deterministicSpanID(seq int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("span|%d", seq)))
	return hex.EncodeToString(sum[:8])
}

// waitUntilSlot sleeps until the schedule slot for request seq under a linear
// RPS ramp from rpsStart→rpsEnd over rampSec, then sustained at rpsEnd.
func waitUntilSlot(start time.Time, seq int, rpsStart, rpsEnd, rampSec float64) {
	if seq <= 1 {
		return
	}
	target := scheduleTime(seq, rpsStart, rpsEnd, rampSec)
	deadline := start.Add(target)
	if d := time.Until(deadline); d > 0 {
		time.Sleep(d)
	}
}

func scheduleTime(seq int, rpsStart, rpsEnd, rampSec float64) time.Duration {
	if rampSec <= 0 || rpsStart == rpsEnd {
		return time.Duration(float64(seq-1) / rpsEnd * float64(time.Second))
	}
	avg := (rpsStart + rpsEnd) / 2
	rampReqs := avg * rampSec
	if float64(seq-1) <= rampReqs {
		// ∫_0^t rps(u) du = seq-1 with rps(u)=start+(end-start)*u/ramp
		a := (rpsEnd - rpsStart) / (2 * rampSec)
		b := rpsStart
		c := -float64(seq - 1)
		disc := b*b - 4*a*c
		t := (-b + math.Sqrt(disc)) / (2 * a)
		if t < 0 {
			t = (-b - math.Sqrt(disc)) / (2 * a)
		}
		return time.Duration(t * float64(time.Second))
	}
	after := float64(seq-1) - rampReqs
	return time.Duration((rampSec + after/rpsEnd) * float64(time.Second))
}

func sendOne(client *http.Client, url, traceID, spanID, stressID, orderID string, seq int) error {
	body := fmt.Sprintf(`{"order_id":%q,"seq":%d}`, orderID, seq)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", fmt.Sprintf("00-%s-%s-01", traceID, spanID))
	req.Header.Set("x-stress-trace-id", stressID)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
