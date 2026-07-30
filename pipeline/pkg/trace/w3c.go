package trace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	HeaderTraceparent  = "traceparent"
	TraceparentVersion = "00"
	TraceparentFlags   = "01"
	traceIDLen         = 32
	spanIDLen          = 16
	versionLen         = 2
	flagsLen           = 2
)

// GenerateTraceID returns a random 32-character lowercase hex string (W3C trace id).
func GenerateTraceID() (string, error) {
	return randomHex(traceIDLen / 2)
}

// GenerateSpanID returns a random 16-character lowercase hex string (W3C span id).
func GenerateSpanID() (string, error) {
	return randomHex(spanIDLen / 2)
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// FormatTraceparent builds a W3C traceparent header (version 00, sampled flag 01).
func FormatTraceparent(traceID, spanID string) string {
	return fmt.Sprintf("%s-%s-%s-%s", TraceparentVersion, traceID, spanID, TraceparentFlags)
}

// ParseTraceparent extracts the trace id from a W3C traceparent value.
// Accepts any 2-character hex version byte (not only 00).
func ParseTraceparent(h string) (traceID string, ok bool) {
	h = strings.TrimSpace(h)
	parts := strings.Split(h, "-")
	if len(parts) != 4 {
		return "", false
	}
	version, tid, sid, flags := parts[0], parts[1], parts[2], parts[3]
	if len(version) != versionLen || len(tid) != traceIDLen || len(sid) != spanIDLen || len(flags) != flagsLen {
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
