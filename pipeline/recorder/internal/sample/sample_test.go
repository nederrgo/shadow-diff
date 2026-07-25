package sample

import "testing"

func TestSampledIn_goldenN10(t *testing.T) {
	if !SampledIn("00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10) {
		t.Fatal("keep")
	}
	if SampledIn("1acccccccccccccccccccccccccccccc", 10) {
		t.Fatal("drop")
	}
	if SampledIn("4Bdddddddddddddddddddddddddddddd", 10) {
		t.Fatal("uppercase drop")
	}
}
