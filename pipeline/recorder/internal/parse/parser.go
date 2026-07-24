package parse

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/shadow-diff/recorder/internal/shop"
)

// RunBidirectional reads paired HTTP transactions from pipe readers and posts to Shop.
func RunBidirectional(ctx context.Context, reqR, resR io.ReadCloser, client *shop.Client) {
	defer reqR.Close()
	defer resR.Close()

	reqReader := bufio.NewReader(reqR)
	resReader := bufio.NewReader(resR)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		req, err := http.ReadRequest(reqReader)
		if err != nil {
			if err != io.EOF {
				log.Printf("recorder parser: ReadRequest error: %v", err)
			}
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()

		host := req.Host
		if host == "" {
			host = req.URL.Host
		}
		if host == "" {
			log.Printf("recorder parser: request missing Host, skipping")
			discardHTTPResponse(resReader, req)
			continue
		}

		path := req.URL.Path
		if path == "" {
			path = "/"
		}
		traceID := traceIDFromTraceparent(req.Header.Get("traceparent"))
		log.Printf("recorder parser: matched request method=%s host=%q path=%s traceID=%s",
			req.Method, NormalizeHTTPHost(host), path, traceID)

		resp, err := http.ReadResponse(resReader, req)
		if err != nil {
			peek, _ := resReader.Peek(120)
			log.Printf("recorder parser: ReadResponse error: %v; resPeek=%q", err, parserPeek(peek))
			return
		}
		respBody, _ := io.ReadAll(resp.Body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		headers := make(map[string]string)
		for k, vals := range resp.Header {
			if len(vals) > 0 {
				headers[k] = vals[0]
			}
		}

		record := shop.RecordPayload{
			TraceID: traceID,
			Method:  req.Method,
			Host:    NormalizeHTTPHost(host),
			Path:    path,
			Response: shop.RecordResponse{
				Status:  resp.StatusCode,
				Headers: headers,
				Body:    string(respBody),
			},
		}
		client.PostAsync(record)
	}
}

// NormalizeHTTPHost lowercases and strips port from host.
func NormalizeHTTPHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return strings.ToLower(h)
	}
	return host
}

// traceIDFromTraceparent extracts the 32-char trace ID from a W3C traceparent header.
// Returns empty string if the header is absent or malformed.
func traceIDFromTraceparent(v string) string {
	if v == "" {
		return ""
	}
	parts := strings.SplitN(v, "-", 3)
	if len(parts) >= 2 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}

func discardHTTPResponse(resReader *bufio.Reader, req *http.Request) {
	resp, err := http.ReadResponse(resReader, req)
	if err != nil {
		return
	}
	_, _ = io.ReadAll(resp.Body)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
