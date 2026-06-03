package session

import (
	"testing"
	"time"
)

func TestFourTuple(t *testing.T) {
	t1 := MakeFourTuple("10.0.0.1", 1234, "10.0.0.2", 80)
	t2 := MakeFourTuple("10.0.0.2", 80, "10.0.0.1", 1234)

	if t1 != t2 {
		t.Errorf("MakeFourTuple should be direction-independent: got %v and %v", t1, t2)
	}

	if t1.IP1 != "10.0.0.1" || t1.IP2 != "10.0.0.2" || t1.Port1 != 1234 || t1.Port2 != 80 {
		t.Errorf("Unexpected sorted fields for FourTuple: %+v", t1)
	}

	str1 := t1.String()
	str2 := t2.String()
	if str1 != str2 {
		t.Errorf("Strings should match: %s and %s", str1, str2)
	}
}

func TestSessionMapStickyAndCap(t *testing.T) {
	sm := NewSessionMap(1*time.Second, 3)

	// Decision must be sticky
	d1 := sm.GetOrDecide("10.0.0.1", 1234, "10.0.0.2", 80, 100) // 100% sample rate should always be true
	d2 := sm.GetOrDecide("10.0.0.2", 80, "10.0.0.1", 1234, 100)

	if !d1 || !d2 {
		t.Errorf("Expected 100%% sample rate to be true, got d1=%v, d2=%v", d1, d2)
	}

	// 0% sample rate should always be false
	d3 := sm.GetOrDecide("10.0.0.3", 5678, "10.0.0.4", 80, 0)
	if d3 {
		t.Error("Expected 0% sample rate to be false")
	}

	// Active count should be 2
	if sm.ActiveCount() != 2 {
		t.Errorf("Expected 2 active sessions, got %d", sm.ActiveCount())
	}

	// Test Capacity Eviction
	// Add more to exceed limit of 3
	sm.GetOrDecide("10.0.0.5", 1111, "10.0.0.6", 80, 50)
	sm.GetOrDecide("10.0.0.7", 2222, "10.0.0.8", 80, 50)

	// Since maxEntries is 3, active count should shrink and not grow unbounded
	if sm.ActiveCount() > 3 {
		t.Errorf("Expected active count <= 3, got %d", sm.ActiveCount())
	}
}
