package trace

import "testing"

func TestParseTraceparent(t *testing.T) {
	id, ok := ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !ok || id != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("parse = %q ok=%v", id, ok)
	}
	if _, ok := ParseTraceparent("bad"); ok {
		t.Fatal("expected invalid traceparent")
	}
}
