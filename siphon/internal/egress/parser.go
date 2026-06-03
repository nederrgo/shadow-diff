package egress

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shadow-diff/siphon/internal/config"
)

// ParseBidirectionalStream reads paired HTTP transactions from pipe readers
// and forwards each pair to Beru.
func ParseBidirectionalStream(sess *Session, forward *Forwarder) {
	target := sess.Target
	if target == nil {
		log.Printf("egress parser: session missing target config")
		return
	}

	deadline := time.After(2 * time.Minute)
	for sess.reqR == nil {
		select {
		case <-deadline:
			log.Printf("egress parser: timed out waiting for request stream")
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
	reqReader := bufio.NewReader(sess.reqR)

	for sess.resR == nil {
		select {
		case <-deadline:
			log.Printf("egress parser: timed out waiting for response stream")
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
	resReader := bufio.NewReader(sess.resR)

	for {
		req, err := http.ReadRequest(reqReader)
		if err != nil {
			if err != io.EOF {
				log.Printf("egress parser: ReadRequest error: %v", err)
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
			log.Printf("egress parser: request missing Host, skipping")
			discardHTTPResponse(resReader, req)
			continue
		}
		if !configHostMatches(host, target.Downstreams) {
			log.Printf("egress parser: host %q not in downstreams, skipping", host)
			discardHTTPResponse(resReader, req)
			continue
		}

		resp, err := http.ReadResponse(resReader, req)
		if err != nil {
			log.Printf("egress parser: ReadResponse error: %v", err)
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

		record := RecordPayload{
			Method:      req.Method,
			Host:        normalizeHTTPHost(host),
			Path:        path,
			Body:        jsonRawBody(reqBody),
			IgnorePaths: ignorePathsForHost(host, target.Downstreams),
			Response: RecordResponse{
				Status:  resp.StatusCode,
				Headers: headers,
				Body:    string(respBody),
			},
		}

		forward.PostAsync(target.BeruHTTPHost, record)
	}
}

func configHostMatches(host string, downstreams []config.SiphonDownstream) bool {
	host = normalizeHTTPHost(host)
	for _, d := range downstreams {
		dh := normalizeHTTPHost(d.Host)
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

func normalizeHTTPHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return strings.ToLower(h)
	}
	return host
}

func ignorePathsForHost(host string, downstreams []config.SiphonDownstream) []string {
	host = normalizeHTTPHost(host)
	for _, d := range downstreams {
		if normalizeHTTPHost(d.Host) == host {
			return d.IgnorePaths
		}
	}
	return nil
}

func jsonRawBody(body []byte) json.RawMessage {
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
