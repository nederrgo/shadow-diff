package replay

// EarlyResponse is a synthetic HTTP response returned on egress mock hit.
type EarlyResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}
