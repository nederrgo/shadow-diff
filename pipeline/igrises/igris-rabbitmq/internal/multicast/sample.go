package multicast

import "strconv"

// sampledIn applies the shared prod-gate sampling rule (same math as Kaisel):
// V = int(traceID[:2], 16); keep iff (V*100) < (N*256).
// Empty/invalid inbound tracing is rejected by the caller before this runs.
// N<=0 or N>=100 keeps all traced traffic.
func sampledIn(traceID string, samplePercentage int) bool {
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
