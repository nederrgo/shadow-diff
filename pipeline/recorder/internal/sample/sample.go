package sample

import (
	"strconv"
	"strings"
)

// SampledIn is the shared prod-gate rule (Pixie PxL + Siphon/Recorder/igris):
// V = int(traceID[:2], 16); keep iff (V*100) < (N*256).
func SampledIn(traceID string, samplePercentage int) bool {
	if samplePercentage <= 0 || samplePercentage >= 100 {
		return true
	}
	if len(traceID) < 2 {
		return false
	}
	v, err := strconv.ParseUint(traceID[:2], 16, 8)
	if err != nil {
		return false
	}
	return v*100 < uint64(samplePercentage)*256
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
