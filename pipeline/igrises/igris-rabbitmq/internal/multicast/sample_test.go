package multicast

import "testing"

// Golden vectors for N=10: keep when V in [0,25] because (V*100) < 2560.
const (
	goldenKeepLow  = "00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // V=0
	goldenKeepHigh = "19bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // V=25
	goldenDropLow  = "1acccccccccccccccccccccccccccccc" // V=26
	goldenDropUp   = "4Bdddddddddddddddddddddddddddddd" // V=0x4B=75, also locks uppercase hex
)

func TestSampledIn_full100NeverDrops(t *testing.T) {
	for _, tid := range []string{goldenKeepLow, goldenDropLow, goldenDropUp} {
		if !sampledIn(tid, 100) {
			t.Fatalf("traceID %q: expected sampled in at 100%%", tid)
		}
	}
}

func TestSampledIn_unsetTreatedAsFull(t *testing.T) {
	if !sampledIn(goldenDropLow, 0) {
		t.Fatal("expected samplePercentage=0 (unset) to behave like 100")
	}
}

func TestSampledIn_goldenN10(t *testing.T) {
	if !sampledIn(goldenKeepLow, 10) {
		t.Fatal("00… should keep at 10%")
	}
	if !sampledIn(goldenKeepHigh, 10) {
		t.Fatal("19… (V=25) should keep at 10%")
	}
	if sampledIn(goldenDropLow, 10) {
		t.Fatal("1a… (V=26) should drop at 10%")
	}
	if sampledIn(goldenDropUp, 10) {
		t.Fatal("4B… uppercase should parse and drop at 10%")
	}
}

func TestSampledIn_n14UnbiasedBoundary(t *testing.T) {
	// (V*100) < (14*256)=3584 → V <= 35 (0x23)
	const keep = "23eeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const drop = "24ffffffffffffffffffffffffffffff"
	if !sampledIn(keep, 14) {
		t.Fatal("V=35 should keep at 14%")
	}
	if sampledIn(drop, 14) {
		t.Fatal("V=36 should drop at 14%")
	}
}

func TestSampledIn_deterministicPerTraceID(t *testing.T) {
	first := sampledIn(goldenKeepLow, 50)
	for i := 0; i < 10; i++ {
		if got := sampledIn(goldenKeepLow, 50); got != first {
			t.Fatalf("iteration %d: got %v, want %v", i, got, first)
		}
	}
}

func TestSampledIn_invalidPrefixDropsWhenSampling(t *testing.T) {
	if sampledIn("zzcccccccccccccccccccccccccccccc", 10) {
		t.Fatal("non-hex prefix should drop when sampling")
	}
	if sampledIn("a", 10) {
		t.Fatal("short id should drop when sampling")
	}
}
