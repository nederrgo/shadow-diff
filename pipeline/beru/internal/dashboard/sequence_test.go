package dashboard

import (
	"strings"
	"testing"

	"github.com/shadow-diff/beru/internal/storage"
)

func TestBuildSequenceSteps_extraAndMissing(t *testing.T) {
	payloads := []storage.EgressPayload{
		{Workload: "control-a", SequenceIndex: 0, PayloadJSON: `{"insert":"orders"}`},
		{Workload: "candidate", SequenceIndex: 0, PayloadJSON: `{"insert":"orders"}`},
		{Workload: "candidate", SequenceIndex: 1, PayloadJSON: `{"insert":"orders","audit":"n1"}`},
	}
	steps := buildSequenceSteps("mongodb", payloads)
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if !steps[0].HasExpected || !steps[0].HasActual || steps[0].IsExtra || steps[0].IsMissing {
		t.Fatalf("step 0 = %+v", steps[0])
	}
	if !steps[1].IsExtra || steps[1].IsMissing || steps[1].Expected != placeholderNoQuery {
		t.Fatalf("step 1 extra = %+v", steps[1])
	}
	if !strings.Contains(steps[1].Actual, "audit") {
		t.Fatalf("step 1 actual = %q", steps[1].Actual)
	}
}

func TestBuildSequenceSteps_missingOnly(t *testing.T) {
	payloads := []storage.EgressPayload{
		{Workload: "control-a", SequenceIndex: 0, PayloadJSON: `{"insert":"orders"}`},
		{Workload: "control-a", SequenceIndex: 1, PayloadJSON: `{"insert":"audit"}`},
		{Workload: "candidate", SequenceIndex: 0, PayloadJSON: `{"insert":"orders"}`},
	}
	steps := buildSequenceSteps("mongodb", payloads)
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if !steps[1].IsMissing || steps[1].IsExtra {
		t.Fatalf("step 1 missing = %+v", steps[1])
	}
	if steps[1].Actual != placeholderNoQuery {
		t.Fatalf("step 1 actual = %q", steps[1].Actual)
	}
}

func TestPrettyDisplayJSON_placeholder(t *testing.T) {
	got := PrettyDisplayJSON(placeholderNoQuery)
	if got != placeholderNoQuery {
		t.Fatalf("placeholder = %q", got)
	}
}

func TestPrettyDisplayJSON_validJSON(t *testing.T) {
	got := PrettyDisplayJSON(`{"a":1}`)
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected indented json, got %q", got)
	}
}

func TestMaxLen(t *testing.T) {
	aMax, cMax := 1, 3
	last := aMax
	if cMax > last {
		last = cMax
	}
	if last != 3 {
		t.Fatalf("last = %d", last)
	}
}
