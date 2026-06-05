package trace

import (
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

const (
	HeaderShadowTraceID = "x-shadow-trace-id"
	HeaderTraceparent   = "traceparent"
	HeaderRequestID     = "x-request-id"
)

// ParseTraceparent extracts the trace id from a W3C traceparent value.
// Accepts any 2-character hex version byte (not only 00).
func ParseTraceparent(h string) (traceID string, ok bool) {
	h = strings.TrimSpace(h)
	parts := strings.Split(h, "-")
	if len(parts) != 4 {
		return "", false
	}
	version, tid, sid, flags := parts[0], parts[1], parts[2], parts[3]
	if len(version) != 2 || len(tid) != 32 || len(sid) != 16 || len(flags) != 2 {
		return "", false
	}
	if !isHex(version) || !isHex(tid) || !isHex(sid) || !isHex(flags) {
		return "", false
	}
	return strings.ToLower(tid), true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// ShadowTraceIDFromMap returns the shadow trace id from a header map using headerValue lookup.
func ShadowTraceIDFromMap(headers *corev3.HeaderMap, headerValue func(*corev3.HeaderMap, string) string) string {
	if headers == nil {
		return ""
	}
	if id := strings.TrimSpace(headerValue(headers, HeaderShadowTraceID)); id != "" {
		return id
	}
	if tp := strings.TrimSpace(headerValue(headers, HeaderTraceparent)); tp != "" {
		if tid, ok := ParseTraceparent(tp); ok {
			return tid
		}
	}
	return strings.TrimSpace(headerValue(headers, HeaderRequestID))
}
