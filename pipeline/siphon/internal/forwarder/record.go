package forwarder

// HTTPRecord is a parsed HTTP request ready to forward to igris-http.
type HTTPRecord struct {
	Method          string
	RequestURI      string // path + optional raw query, e.g. /v1/users?active=true
	Host            string
	Body            []byte
	ShadowTraceID   string
	Traceparent     string
}
