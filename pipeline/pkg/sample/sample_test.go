package sample

import "testing"

// Golden vectors for N=10 under FNV-1a-64(full 16-byte id) & 0xFF:
// keep when V in [0,25] because (V*100) < 2560.
const (
	goldenKeepLow  = "00000000000000000000000000000087" // V=0
	goldenKeepHigh = "00000000000000000000000000000084" // V=25
	goldenDropLow  = "000000000000000000000000000000f9" // V=26
	goldenDropUp   = "0000000000000000000000000000ABCD" // uppercase hex; V>=26
)

func TestSampledIn_full100NeverDrops(t *testing.T) {
	for _, tid := range []string{goldenKeepLow, goldenDropLow, goldenDropUp} {
		if !SampledIn(tid, 100) {
			t.Fatalf("traceID %q: expected sampled in at 100%%", tid)
		}
	}
}

func TestSampledIn_unsetTreatedAsFull(t *testing.T) {
	if !SampledIn(goldenDropLow, 0) {
		t.Fatal("expected samplePercentage=0 (unset) to behave like 100")
	}
}

func TestSampledIn_goldenN10(t *testing.T) {
	if !SampledIn(goldenKeepLow, 10) {
		t.Fatal("V=0 should keep at 10%")
	}
	if !SampledIn(goldenKeepHigh, 10) {
		t.Fatal("V=25 should keep at 10%")
	}
	if SampledIn(goldenDropLow, 10) {
		t.Fatal("V=26 should drop at 10%")
	}
	if SampledIn(goldenDropUp, 10) {
		t.Fatal("uppercase hex should decode and drop at 10%")
	}
}

func TestSampledIn_deterministicPerTraceID(t *testing.T) {
	first := SampledIn(goldenKeepLow, 50)
	for i := 0; i < 10; i++ {
		if got := SampledIn(goldenKeepLow, 50); got != first {
			t.Fatalf("iteration %d: got %v, want %v", i, got, first)
		}
	}
}

func TestSampledIn_invalidDropsWhenSampling(t *testing.T) {
	if SampledIn("zzcccccccccccccccccccccccccccccc", 10) {
		t.Fatal("non-hex id should drop when sampling")
	}
	if SampledIn("a", 10) {
		t.Fatal("short id should drop when sampling")
	}
	if SampledIn("0000000000000000000000000000000", 10) {
		t.Fatal("31-char id should drop when sampling")
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
