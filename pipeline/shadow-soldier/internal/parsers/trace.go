package parsers

import "regexp"

// traceparentRE matches a W3C traceparent wherever it appears as plaintext in a
// wire payload: a SQL comment (`/* traceparent='00-...' */`), a BSON $comment
// field, or a TDS batch comment.
//
// Scanning raw bytes rather than parsing each protocol's comment syntax is the
// mechanism Beru already uses for MongoDB spans, and it costs one regex instead
// of four per-protocol extractors.
//
// Version is pinned to `00` — the only version W3C defines — because an
// unanchored 2-hex version would match inside any sufficiently long hex string.
var traceparentRE = regexp.MustCompile(`00-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}`)

// TraceIDFrom returns the bare 32-hex lowercase trace id embedded in b, or ""
// when b carries no traceparent.
//
// Beru stores trace_id verbatim as its SQLite grouping key, so callers must send
// this bare id rather than the full traceparent — otherwise reports from this
// sidecar land in a different bucket than reports from Envoy's ext_proc path.
func TraceIDFrom(b []byte) string {
	m := traceparentRE.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}
