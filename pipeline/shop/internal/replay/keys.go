package replay

import (
	"net"
	"strings"
)

// TraceKey returns the mock store key for a trace-ID-keyed record.
// host must be pre-normalized with HostWithoutPort so seed and lookup keys match.
// The "trace:" prefix ensures no collision with legacy body-hash keys.
func TraceKey(traceID, method, host, path string) string {
	return "trace:" + traceID + ":" + strings.ToUpper(method) + ":" + host + ":" + path
}

// HostWithoutPort strips the port from a host:port string, returning just the host.
// Used by both the seed endpoint and the ext_proc lookup to produce a consistent key.
func HostWithoutPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
