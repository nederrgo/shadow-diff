package parse

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
)

// RunBidirectional reads paired HTTP transactions from pipe readers and posts to Beru.
func RunBidirectional(ctx context.Context, reqR, resR io.ReadCloser, recordAndReplay []config.RecordAndReplayHost, client *beru.Client) {
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
		reqBody, _ := io.ReadAll(req.Body)
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
		if !HostMatches(host, recordAndReplay) {
			log.Printf("recorder parser: host %q not in recordAndReplay, skipping", host)
			discardHTTPResponse(resReader, req)
			continue
		}

		resp, err := http.ReadResponse(resReader, req)
		if err != nil {
			log.Printf("recorder parser: ReadResponse error: %v", err)
			return
		}
		respBody, _ := io.ReadAll(resp.Body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		path := req.URL.Path
		if path == "" {
			path = "/"
		}

		headers := make(map[string]string)
		for k, vals := range resp.Header {
			if len(vals) > 0 {
				headers[k] = vals[0]
			}
		}

		record := beru.RecordPayload{
			Method:      req.Method,
			Host:        NormalizeHTTPHost(host),
			Path:        path,
			Body:        JSONRawBody(reqBody),
			IgnorePaths: IgnorePathsForHost(host, recordAndReplay),
			Response: beru.RecordResponse{
				Status:  resp.StatusCode,
				Headers: headers,
				Body:    string(respBody),
			},
		}
		client.PostAsync(record)
	}
}

// HostMatches reports whether host is allowed by downstream rules.
func HostMatches(host string, recordAndReplay []config.RecordAndReplayHost) bool {
	host = NormalizeHTTPHost(host)
	for _, d := range recordAndReplay {
		dh := NormalizeHTTPHost(d.Host)
		if dh == host {
			return true
		}
		if strings.HasPrefix(dh, "*.") {
			suffix := strings.TrimPrefix(dh, "*")
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
	}
	return false
}

// NormalizeHTTPHost lowercases and strips port from host.
func NormalizeHTTPHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return strings.ToLower(h)
	}
	return host
}

// IgnorePathsForHost returns ignore_paths for a matching downstream host.
func IgnorePathsForHost(host string, recordAndReplay []config.RecordAndReplayHost) []string {
	host = NormalizeHTTPHost(host)
	for _, d := range recordAndReplay {
		if NormalizeHTTPHost(d.Host) == host {
			return d.IgnorePaths
		}
	}
	return nil
}

// JSONRawBody normalizes a request body for JSON encoding.
func JSONRawBody(body []byte) json.RawMessage {
	if len(body) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	quoted, _ := json.Marshal(string(body))
	return json.RawMessage(quoted)
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
