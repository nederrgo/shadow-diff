package capture

// IngressCapture is the JSONL shape written to S3 in record mode and replayed later.
type IngressCapture struct {
	Traceparent  string            `json:"traceparent"`
	TraceID      string            `json:"trace_id"`
	RoutingKey   string            `json:"routing_key"`
	Exchange     string            `json:"exchange"`
	ContentType  string            `json:"content_type"`
	DeliveryMode uint8             `json:"delivery_mode"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         []byte            `json:"body"`
}
