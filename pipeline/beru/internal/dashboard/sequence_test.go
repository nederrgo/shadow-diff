package dashboard

import (
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

func TestBuildSequenceSteps_extraAndMissing(t *testing.T) {
	reports := []storage.RawReport{
		{ShadowRole: "control-a", Protocol: "mongodb", Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders"}`), CapturedAt: time.Now()},
		{ShadowRole: "candidate", Protocol: "mongodb", Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders"}`), CapturedAt: time.Now()},
		{ShadowRole: "candidate", Protocol: "mongodb", Signature: "mongodb:insert:audit", PayloadBytes: []byte(`{"insert":"orders","audit":"n1"}`), CapturedAt: time.Now()},
	}
	steps := buildSequenceStepsFromReports("mongodb", reports)
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if !steps[0].HasExpected || !steps[0].HasActual || steps[0].IsExtra || steps[0].IsMissing {
		t.Fatalf("step 0 = %+v", steps[0])
	}
	if !steps[1].IsExtra || steps[1].IsMissing || steps[1].Expected != placeholderNoQuery {
		t.Fatalf("step 1 extra = %+v", steps[1])
	}
}

func TestBuildSequenceSteps_missingOnly(t *testing.T) {
	reports := []storage.RawReport{
		{ShadowRole: "control-a", Protocol: "mongodb", PayloadBytes: []byte(`{"insert":"orders"}`), CapturedAt: time.Now()},
		{ShadowRole: "control-a", Protocol: "mongodb", PayloadBytes: []byte(`{"insert":"audit"}`), CapturedAt: time.Now()},
		{ShadowRole: "candidate", Protocol: "mongodb", PayloadBytes: []byte(`{"insert":"orders"}`), CapturedAt: time.Now()},
	}
	steps := buildSequenceStepsFromReports("mongodb", reports)
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if !steps[1].IsMissing || steps[1].IsExtra {
		t.Fatalf("step 1 missing = %+v", steps[1])
	}
}
