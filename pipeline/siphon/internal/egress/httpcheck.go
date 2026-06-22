package egress

import (
	"bytes"
	"log"
	"strconv"
	"strings"
)

// httpLegComplete reports whether buf holds a full HTTP message (headers + Content-Length body).
// ponytail: chunked / no CL treated complete once headers end — upgrade for chunked replay later.
func httpLegComplete(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	idx := bytes.Index(buf, []byte("\r\n\r\n"))
	if idx < 0 {
		return false
	}
	head := buf[:idx]
	body := buf[idx+4:]
	cl := parseContentLength(head)
	if cl < 0 {
		return true
	}
	return len(body) >= cl
}

func isHTTPLeg(buf []byte) bool {
	if len(buf) < 4 {
		return false
	}
	switch {
	case bytes.HasPrefix(buf, []byte("GET ")),
		bytes.HasPrefix(buf, []byte("POST ")),
		bytes.HasPrefix(buf, []byte("PUT ")),
		bytes.HasPrefix(buf, []byte("PATCH ")),
		bytes.HasPrefix(buf, []byte("DELETE ")),
		bytes.HasPrefix(buf, []byte("HEAD ")),
		bytes.HasPrefix(buf, []byte("HTTP/")):
		return true
	default:
		return false
	}
}

func httpHeadersComplete(buf []byte) bool {
	return bytes.Index(buf, []byte("\r\n\r\n")) >= 0
}

// httpBodyShortfall reports declared Content-Length vs captured body when headers are parsed.
func httpBodyShortfall(buf []byte) (declared, have int, ok bool) {
	idx := bytes.Index(buf, []byte("\r\n\r\n"))
	if idx < 0 {
		return 0, 0, false
	}
	cl := parseContentLength(buf[:idx])
	if cl < 0 {
		return 0, 0, false
	}
	have = len(buf[idx+4:])
	if have >= cl {
		return cl, have, false
	}
	return cl, have, true
}

func parseContentLength(head []byte) int {
	for line := range strings.SplitSeq(string(head), "\r\n") {
		if len(line) < 15 {
			continue
		}
		if strings.EqualFold(line[:15], "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(line[15:]))
			if err != nil || n < 0 {
				return -1
			}
			return n
		}
	}
	return -1
}

// applyRequestTruncationFallback rewrites Content-Length to match captured body on truncated PCA snaps.
func applyRequestTruncationFallback(buf []byte) ([]byte, bool) {
	if httpLegComplete(buf) {
		return buf, false
	}
	if out, ok := applyCLShortfallFallback(buf); ok {
		return out, true
	}
	return applyPartialHeadersFallback(buf)
}

func applyCLShortfallFallback(buf []byte) ([]byte, bool) {
	_, _, shortfall := httpBodyShortfall(buf)
	if !shortfall {
		return buf, false
	}
	out := normalizeTruncatedHTTP(buf)
	if !httpLegComplete(out) {
		return out, false
	}
	logTruncationFallback(out)
	return out, true
}

// applyPartialHeadersFallback handles PCA snaps that include Content-Length + JSON body but no header terminator yet.
func applyPartialHeadersFallback(buf []byte) ([]byte, bool) {
	if !bytes.Contains(buf, []byte("Content-Length:")) || !bytes.Contains(buf, []byte("{")) {
		return buf, false
	}
	jsonStart := bytes.Index(buf, []byte("{"))
	if jsonStart < 0 {
		return buf, false
	}
	head := append([]byte(nil), buf[:jsonStart]...)
	body := append([]byte(nil), buf[jsonStart:]...)
	if !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		switch {
		case bytes.HasSuffix(head, []byte("\r\n\r")):
			head = append(head, '\n')
		case bytes.HasSuffix(head, []byte("\r\n")):
			head = append(head, '\r', '\n')
		default:
			head = append(head, '\r', '\n', '\r', '\n')
		}
	}
	head = bytes.TrimSuffix(head, []byte("\r\n\r\n"))
	newHead := rewriteContentLength(head, len(body))
	out := make([]byte, 0, len(newHead)+4+len(body))
	out = append(out, newHead...)
	out = append(out, '\r', '\n', '\r', '\n')
	out = append(out, body...)
	if !httpLegComplete(out) {
		return out, false
	}
	logTruncationFallback(out)
	return out, true
}

func logTruncationFallback(out []byte) {
	log.Printf("Siphon stream truncation fallback triggered. Rewriting Content-Length. Length: %d, Hex: %x", len(out), out)
}

// normalizeTruncatedHTTP fixes Content-Length when PCA delivered fewer body bytes than declared.
func normalizeTruncatedHTTP(buf []byte) []byte {
	if httpLegComplete(buf) {
		return buf
	}
	idx := bytes.Index(buf, []byte("\r\n\r\n"))
	if idx < 0 {
		return buf
	}
	head, body := buf[:idx], buf[idx+4:]
	cl := parseContentLength(head)
	if cl < 0 || len(body) >= cl {
		return buf
	}
	newHead := rewriteContentLength(head, len(body))
	out := make([]byte, 0, len(newHead)+4+len(body))
	out = append(out, newHead...)
	out = append(out, '\r', '\n', '\r', '\n')
	out = append(out, body...)
	return out
}

// ponytail: PCA snap ~190B often lands mid-header (httpbin headers ~210B); synthesize parseable HTTP for recorder.
var pcaCappedResponseFallback = []byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

// finalizeTruncatedRequest shrinks Content-Length when PCA delivered fewer request body bytes than declared.
func finalizeTruncatedRequest(buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}
	out, _ := applyRequestTruncationFallback(buf)
	return out
}

// finalizeTruncatedResponse returns bytes recorder can parse after PCA FIN.
func finalizeTruncatedResponse(buf []byte) []byte {
	if len(buf) == 0 {
		return append([]byte(nil), pcaCappedResponseFallback...)
	}
	if httpLegComplete(buf) {
		return buf
	}
	fixed := normalizeTruncatedHTTP(buf)
	if httpLegComplete(fixed) {
		return fixed
	}
	if bytes.Index(buf, []byte("\r\n\r\n")) < 0 {
		return append([]byte(nil), pcaCappedResponseFallback...)
	}
	return fixed
}

func rewriteContentLength(head []byte, n int) []byte {
	lines := strings.Split(string(head), "\r\n")
	for i, line := range lines {
		if len(line) >= 15 && strings.EqualFold(line[:15], "content-length:") {
			lines[i] = "Content-Length: " + strconv.Itoa(n)
			return []byte(strings.Join(lines, "\r\n"))
		}
	}
	return head
}
