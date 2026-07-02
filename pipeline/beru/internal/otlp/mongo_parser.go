package otlp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var traceparentRE = regexp.MustCompile(`00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}`)

// ExtractTraceparentFromRaw searches for a W3C traceparent embedded in raw MongoDB wire bytes.
// Pixie captures raw TCP payload; the traceparent appears as ASCII text in the BSON $comment field.
func ExtractTraceparentFromRaw(raw string) string {
	return traceparentRE.FindString(raw)
}

// ParseMongoStatement normalizes an OpenTelemetry db.statement value to canonical JSON bytes.
func ParseMongoStatement(statement string) ([]byte, error) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, fmt.Errorf("empty db.statement")
	}

	var raw json.RawMessage
	if err := json.Unmarshal([]byte(statement), &raw); err != nil {
		// ponytail: non-JSON wire text wrapped for diff; upgrade path is driver-specific parsers
		return json.Marshal(map[string]string{"query": statement})
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.Marshal(map[string]string{"query": statement})
	}
	normalized := normalizeMongoValue(v)
	return json.Marshal(normalized)
}

func normalizeMongoValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if oid, ok := extendedString(t, "$oid"); ok {
			return oid
		}
		if date, ok := extendedValueString(t, "$date"); ok {
			return date
		}
		if len(t) == 1 {
			for k := range t {
				if strings.HasPrefix(k, "$") {
					if b, err := json.Marshal(t); err == nil {
						return string(b)
					}
				}
			}
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeMongoValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeMongoValue(val)
		}
		return out
	default:
		return v
	}
}

func extendedString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok || len(m) != 1 {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

func extendedValueString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok || len(m) != 1 {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, t != ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}
