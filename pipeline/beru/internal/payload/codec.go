package payload

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Codec normalizes a payload body for stable comparison.
type Codec interface {
	Name() string
	Normalize(body []byte, metadata map[string]string) ([]byte, error)
}

// Registry selects a codec by content type and metadata.
type Registry struct {
	defaultCodec Codec
	codecs       []Codec
}

func NewRegistry() *Registry {
	return &Registry{
		defaultCodec: &JSONCodec{},
		codecs:       []Codec{&JSONCodec{}},
	}
}

func (r *Registry) Normalize(body []byte, metadata map[string]string, contentType string) ([]byte, string, error) {
	c := r.selectCodec(contentType, body)
	out, err := c.Normalize(body, metadata)
	if err != nil {
		return nil, c.Name(), err
	}
	return out, c.Name(), nil
}

func (r *Registry) selectCodec(contentType string, body []byte) Codec {
	for _, c := range r.codecs {
		if c.Name() == "json" && looksJSON(contentType, body) {
			return c
		}
	}
	return &RawCodec{}
}

func looksJSON(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") {
		return true
	}
	trim := bytes.TrimSpace(body)
	return len(trim) > 0 && (trim[0] == '{' || trim[0] == '[')
}

// JSONCodec normalizes JSON bodies to compact form.
type JSONCodec struct{}

func (JSONCodec) Name() string { return "json" }

func (JSONCodec) Normalize(body []byte, _ map[string]string) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("{}"), nil
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid JSON")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RawCodec passes through non-JSON payloads.
type RawCodec struct{}

func (RawCodec) Name() string { return "raw" }

func (RawCodec) Normalize(body []byte, _ map[string]string) ([]byte, error) {
	return bytes.Clone(body), nil
}
