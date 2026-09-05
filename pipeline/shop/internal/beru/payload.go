package beru

import "encoding/json"

// BuildHTTPEgressPayload builds the /api/v1/egress/diff payload for an HTTP egress call.
// Body is embedded JSON when valid, otherwise a JSON string (empty body → "").
func BuildHTTPEgressPayload(method, host, path string, status int, body []byte) (json.RawMessage, error) {
	var bodyJSON json.RawMessage
	switch {
	case len(body) == 0:
		bodyJSON = json.RawMessage(`""`)
	case json.Valid(body):
		bodyJSON = json.RawMessage(append([]byte(nil), body...))
	default:
		raw, err := json.Marshal(string(body))
		if err != nil {
			return nil, err
		}
		bodyJSON = raw
	}
	return json.Marshal(struct {
		Method string          `json:"method"`
		Host   string          `json:"host"`
		Path   string          `json:"path"`
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}{
		Method: method,
		Host:   host,
		Path:   path,
		Status: status,
		Body:   bodyJSON,
	})
}
