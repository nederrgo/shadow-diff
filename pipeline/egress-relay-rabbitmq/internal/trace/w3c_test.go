package trace

import "testing"

func TestParseTraceparent(t *testing.T) {
	id, span, ok := ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !ok || id != "4bf92f3577b34da6a3ce929d0e0e4736" || span != "00f067aa0ba902b7" {
		t.Fatalf("parse trace=%q span=%q ok=%v", id, span, ok)
	}
	if _, _, ok := ParseTraceparent("bad"); ok {
		t.Fatal("expected invalid traceparent")
	}
}
