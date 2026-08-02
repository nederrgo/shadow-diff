package db

import (
	"encoding/json"
	"testing"
)

func TestAlignOccurrences_zipsRolesByIndex(t *testing.T) {
	rows := []rawRolePayload{
		{Role: "control-a", Payload: []byte(`{"n":1}`)},
		{Role: "control-b", Payload: []byte(`{"n":1}`)},
		{Role: "candidate", Payload: []byte(`{"n":1}`)},
		{Role: "control-a", Payload: []byte(`{"n":2}`)},
		{Role: "control-b", Payload: []byte(`{"n":2}`)},
		{Role: "candidate", Payload: []byte(`{"n":9}`)},
	}
	got, truncated := alignOccurrences(rows, 50)
	if truncated || len(got) != 2 {
		t.Fatalf("got len=%d truncated=%v", len(got), truncated)
	}
	if string(got[0].ControlAPayload) != `{"n":1}` || string(got[1].CandidatePayload) != `{"n":9}` {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Fatalf("indexes = %d %d", got[0].Index, got[1].Index)
	}
}

func TestAlignOccurrences_missingRoleLeavesNil(t *testing.T) {
	rows := []rawRolePayload{
		{Role: "control-a", Payload: []byte(`{"a":1}`)},
		{Role: "control-a", Payload: []byte(`{"a":2}`)},
		{Role: "candidate", Payload: []byte(`{"c":1}`)},
	}
	got, _ := alignOccurrences(rows, 50)
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ControlBPayload != nil || got[1].CandidatePayload != nil {
		t.Fatalf("expected nils: %+v", got)
	}
	if string(got[1].ControlAPayload) != `{"a":2}` {
		t.Fatalf("control-a[1] = %s", got[1].ControlAPayload)
	}
}

func TestAlignOccurrences_truncates(t *testing.T) {
	var rows []rawRolePayload
	for i := 0; i < 5; i++ {
		rows = append(rows, rawRolePayload{Role: "control-a", Payload: []byte(`{}`)})
	}
	got, truncated := alignOccurrences(rows, 3)
	if !truncated || len(got) != 3 {
		t.Fatalf("len=%d truncated=%v", len(got), truncated)
	}
}

func TestDecodePayloadBytes_rawWrap(t *testing.T) {
	raw := decodePayloadBytes([]byte("not-json"))
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil || m["_raw"] != "not-json" {
		t.Fatalf("got %s err=%v", raw, err)
	}
	if decodePayloadBytes(nil) != nil {
		t.Fatal("empty should be nil")
	}
}
