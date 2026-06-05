package diff

import "testing"

func TestMergeNoise_userFilters(t *testing.T) {
	ab := map[string]struct{}{"timestamp": {}}
	user := map[string]struct{}{"price": {}}
	merged := MergeNoise(ab, user)
	if _, ok := merged["timestamp"]; !ok {
		t.Fatal("expected timestamp in merged noise")
	}
	if _, ok := merged["price"]; !ok {
		t.Fatal("expected price in merged noise")
	}

	bodyA := []byte(`{"price":10,"timestamp":"t1"}`)
	bodyC := []byte(`{"price":12,"timestamp":"t1"}`)
	regs, err := Regressions(bodyA, bodyC, merged)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("expected user noise to suppress price regression, got %v", regs)
	}
}

func TestAnalyze_returnsResult(t *testing.T) {
	bodyA := []byte(`{"price":10.0,"timestamp":"t1"}`)
	bodyB := []byte(`{"price":10.0,"timestamp":"t2"}`)
	bodyC := []byte(`{"price":12.0,"timestamp":"t1"}`)
	res := Analyze(nil, "abc", ProtocolIngress, bodyA, bodyB, bodyC, nil)
	if res.Status != StatusMismatch {
		t.Fatalf("status = %q", res.Status)
	}
	if len(res.Regressions) != 1 {
		t.Fatalf("regressions = %d", len(res.Regressions))
	}
}
