package replay

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/sjson"
)

// stripJSONPaths removes each JSONPath from body and returns the modified bytes.
// Paths use gjson/sjson syntax (e.g. "$.timestamp").
func stripJSONPaths(body []byte, paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return body, nil
	}
	out := string(body)
	var err error
	for _, p := range paths {
		p = normalizeJSONPath(p)
		if p == "" {
			continue
		}
		out, err = sjson.Delete(out, p)
		if err != nil {
			return nil, err
		}
	}
	return []byte(out), nil
}

// normalizeJSONPath converts JSONPath-style "$.a.b" to gjson path "a.b".
func normalizeJSONPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "$.")
	if p == "$" {
		return ""
	}
	return strings.TrimPrefix(p, "$")
}

// compactJSON removes all whitespace from valid JSON.
func compactJSON(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return []byte{}, nil
	}
	if !json.Valid(body) {
		return body, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
