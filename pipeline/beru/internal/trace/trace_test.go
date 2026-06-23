package trace

import (
	"strings"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

func testHeaderValue(headers *corev3.HeaderMap, key string) string {
	if headers == nil {
		return ""
	}
	kl := strings.ToLower(key)
	for _, h := range headers.Headers {
		if strings.ToLower(h.Key) == kl {
			if len(h.RawValue) > 0 {
				return string(h.RawValue)
			}
			return h.Value
		}
	}
	return ""
}

func TestParseTraceparent_version01(t *testing.T) {
	tid, ok := ParseTraceparent("01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	if !ok || tid != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got %q ok=%v", tid, ok)
	}
}

func TestShadowTraceIDFromMap_priority(t *testing.T) {
	t.Parallel()
	hdrs := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "x-shadow-trace-id", Value: "custom-id"},
			{Key: "traceparent", Value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		},
	}
	if got := ShadowTraceIDFromMap(hdrs, testHeaderValue); got != "custom-id" {
		t.Fatalf("got %q", got)
	}
}

func TestShadowTraceIDFromMap_traceparentOnly(t *testing.T) {
	t.Parallel()
	hdrs := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "traceparent", Value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		},
	}
	if got := ShadowTraceIDFromMap(hdrs, testHeaderValue); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("got %q", got)
	}
}

func TestShadowTraceIDFromMap_requestIDFallback(t *testing.T) {
	t.Parallel()
	hdrs := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "x-request-id", Value: "req-99"},
		},
	}
	if got := ShadowTraceIDFromMap(hdrs, testHeaderValue); got != "req-99" {
		t.Fatalf("got %q", got)
	}
}
