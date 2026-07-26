package sample

import (
	"encoding/hex"
	"hash/fnv"
	"strings"
)

// SampledIn is the shared prod-gate rule (Kaisel + igris-rabbitmq):
// decode the 32-hex W3C trace id to 16 bytes, V = FNV-1a-64(bytes) & 0xFF,
// keep iff (V*100) < (N*256).
// Callers must drop empty/invalid tracing before invoking this.
// N<=0 or N>=100 keeps all traced traffic.
func SampledIn(traceID string, samplePercentage int) bool {
	if samplePercentage <= 0 || samplePercentage >= 100 {
		return true
	}
	v, ok := bucket(traceID)
	if !ok {
		return false
	}
	return uint64(v)*100 < uint64(samplePercentage)*256
}

func bucket(traceID string) (uint8, bool) {
	if len(traceID) != 32 {
		return 0, false
	}
	raw, err := hex.DecodeString(strings.ToLower(traceID))
	if err != nil || len(raw) != 16 {
		return 0, false
	}
	h := fnv.New64a()
	_, _ = h.Write(raw)
	return uint8(h.Sum64() & 0xff), true
}

// TraceIDFromTraceparent extracts the 32-char trace id from a W3C traceparent.
func TraceIDFromTraceparent(h string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) != 4 {
		return "", false
	}
	tid := parts[1]
	if len(tid) != 32 {
		return "", false
	}
	for i := 0; i < len(tid); i++ {
		c := tid[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return "", false
	}
	return tid, true
}
