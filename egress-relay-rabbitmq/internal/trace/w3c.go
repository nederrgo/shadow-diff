package trace

import (
	"strings"
)

// ParseTraceparent extracts the trace id from a W3C traceparent value.
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
