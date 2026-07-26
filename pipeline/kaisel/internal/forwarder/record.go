package forwarder

import "net/http"

// HTTPRecord is a parsed HTTP request ready to forward to igris-http.
type HTTPRecord struct {
	Method      string
	RequestURI  string // path + optional raw query, e.g. /v1/users?active=true
	Host        string
	Body        []byte
	Traceparent string
	// Headers are the captured request headers (hop-by-hop stripped).
	// Host and Content-Length are omitted; Traceparent is also set from the
	// dedicated field so admit/sample stays authoritative.
	Headers http.Header
}
