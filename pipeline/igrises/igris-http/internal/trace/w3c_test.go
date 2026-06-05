package trace

import (
	"strings"
	"testing"
)

func TestFormatTraceparent(t *testing.T) {
	t.Parallel()
	tp := FormatTraceparent("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
	want := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if tp != want {
		t.Fatalf("got %q want %q", tp, want)
	}
}

func TestParseTraceparent_version00(t *testing.T) {
	t.Parallel()
	tid, ok := ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !ok || tid != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("got tid=%q ok=%v", tid, ok)
	}
}

func TestParseTraceparent_version01(t *testing.T) {
	t.Parallel()
	tid, ok := ParseTraceparent("01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	if !ok || tid != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got tid=%q ok=%v", tid, ok)
	}
}

func TestParseTraceparent_rejectsInvalid(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"00-short-span-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
		"00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-gg",
	}
	for _, c := range cases {
		if tid, ok := ParseTraceparent(c); ok {
			t.Fatalf("ParseTraceparent(%q) = %q, true; want false", c, tid)
		}
	}
}

func TestGenerateTraceAndSpanID(t *testing.T) {
	t.Parallel()
	tid, err := GenerateTraceID()
	if err != nil {
		t.Fatal(err)
	}
	if len(tid) != traceIDLen {
		t.Fatalf("trace id len %d", len(tid))
	}
	sid, err := GenerateSpanID()
	if err != nil {
		t.Fatal(err)
	}
	if len(sid) != spanIDLen {
		t.Fatalf("span id len %d", len(sid))
	}
	tp := FormatTraceparent(tid, sid)
	if !strings.HasPrefix(tp, "00-") || !strings.HasSuffix(tp, "-01") {
		t.Fatalf("traceparent %q", tp)
	}
	parsed, ok := ParseTraceparent(tp)
	if !ok || parsed != tid {
		t.Fatalf("round-trip: parsed=%q ok=%v", parsed, ok)
	}
}
