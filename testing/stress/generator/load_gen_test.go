package main

import (
	"math"
	"testing"
	"time"
)

func TestDeterministicTraceIDStable(t *testing.T) {
	a := deterministicTraceID("run-abc", 1)
	b := deterministicTraceID("run-abc", 1)
	c := deterministicTraceID("run-abc", 2)
	if a != b {
		t.Fatalf("same inputs must match: %s vs %s", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("trace_id len=%d want 32", len(a))
	}
	if a == c {
		t.Fatal("different seq must differ")
	}
}

func TestScheduleTimeConstantRPS(t *testing.T) {
	d := scheduleTime(101, 100, 100, 0)
	want := time.Second // (101-1)/100
	if math.Abs(d.Seconds()-want.Seconds()) > 1e-9 {
		t.Fatalf("got %v want %v", d, want)
	}
}

func TestScheduleTimeRampThenSustain(t *testing.T) {
	// 10→20 RPS over 2s → avg 15 * 2 = 30 requests in ramp window.
	d30 := scheduleTime(31, 10, 20, 2) // last request still in/at ramp edge
	d31 := scheduleTime(32, 10, 20, 2) // first after ramp
	if d30 > d31 {
		t.Fatalf("schedule must be monotonic: %v > %v", d30, d31)
	}
	if d31.Seconds() < 2 {
		t.Fatalf("post-ramp request should be after rampSec: got %v", d31)
	}
}
