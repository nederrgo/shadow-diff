package sample

import "testing"

const (
	goldenKeepLow  = "00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	goldenKeepHigh = "19bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	goldenDropLow  = "1acccccccccccccccccccccccccccccc"
	goldenDropUp   = "4Bdddddddddddddddddddddddddddddd"
)

func TestSampledIn_goldenN10(t *testing.T) {
	if !SampledIn(goldenKeepLow, 10) || !SampledIn(goldenKeepHigh, 10) {
		t.Fatal("expected keep at 10%")
	}
	if SampledIn(goldenDropLow, 10) || SampledIn(goldenDropUp, 10) {
		t.Fatal("expected drop at 10%")
	}
}

func TestTraceIDFromTraceparent(t *testing.T) {
	tid, ok := TraceIDFromTraceparent("00-" + goldenDropUp + "-00f067aa0ba902b7-01")
	if !ok || tid != goldenDropUp {
		t.Fatalf("got %q ok=%v", tid, ok)
	}
	if _, ok := TraceIDFromTraceparent(""); ok {
		t.Fatal("empty should fail")
	}
}
