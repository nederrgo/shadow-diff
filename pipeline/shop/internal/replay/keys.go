package replay

import "strings"

// TraceKey returns the mock store key for a trace-ID-keyed record.
// The "trace:" prefix ensures no collision with legacy body-hash keys.
func TraceKey(traceID, method, host, path string) string {
	return "trace:" + traceID + ":" + strings.ToUpper(method) + ":" + host + ":" + path
}
