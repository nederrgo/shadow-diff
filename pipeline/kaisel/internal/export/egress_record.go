package export

import (
	"net/http"
	"strings"
)

// EgressRecord is the JSON body for Shop's POST /v1/record_egress. The shape
// mirrors Shop's own seedMockRequest exactly.
//
// Host, Method and Path are sent verbatim off the wire. Shop derives the mock
// key -- upper-casing the method and stripping the host's port -- and applies
// the identical transform on the ext_proc lookup path, so normalizing here
// could only introduce a mismatch. Lowercasing the host in particular would:
// the lookup side never lowercases, so a lower-cased key for a mixed-case
// authority is one Envoy can never generate.
//
// Path carries the query string, matching Envoy's :path pseudo-header, which
// is what the lookup side actually keys on.
type EgressRecord struct {
	TraceID  string         `json:"trace_id"`
	Method   string         `json:"method"`
	Host     string         `json:"host"`
	Path     string         `json:"path"`
	Response EgressResponse `json:"response"`
}

// EgressResponse is the recorded response half of the transaction.
type EgressResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// droppedResponseHeaders never reach a replayed mock.
//
// The framing headers are dropped because Envoy synthesizes its own on the
// ext_proc immediate-response path: a captured Content-Length that no longer
// matches the replayed body truncates or hangs it. The hop-by-hop headers
// describe the captured connection, not the replayed one. Date and Server
// differ on every capture and would surface as diff noise attributable to
// nothing.
var droppedResponseHeaders = map[string]bool{
	"content-length":    true,
	"transfer-encoding": true,
	"connection":        true,
	"keep-alive":        true,
	"te":                true,
	"trailer":           true,
	"upgrade":           true,
	"date":              true,
	"server":            true,
}

// keptResponseHeaders are the semantic headers a shadow client may need to
// parse the body correctly.
var keptResponseHeaders = map[string]bool{
	"content-type":  true,
	"cache-control": true,
	"etag":          true,
	"location":      true,
}

// filterResponseHeaders selects the headers safe to replay.
//
// ponytail: allowlist plus an x-* passthrough, not a full header model. The
// ceiling is that an app relying on some other standard header (say
// Content-Encoding) silently loses it; the upgrade path is adding it above.
// Multi-value headers collapse to the first value because Shop's stored
// EarlyResponse.Headers is a map[string]string.
func filterResponseHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if len(vals) == 0 {
			continue
		}
		lower := strings.ToLower(k)
		if droppedResponseHeaders[lower] {
			continue
		}
		if !keptResponseHeaders[lower] && !strings.HasPrefix(lower, "x-") {
			continue
		}
		out[lower] = vals[0]
	}
	return out
}
