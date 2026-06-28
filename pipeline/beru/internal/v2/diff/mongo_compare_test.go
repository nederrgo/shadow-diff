package diff

import "testing"

func TestMongoPayloadsEqual_ignoresID(t *testing.T) {
	a := []byte(`{"_id":"aaa","e2e":"x","source":"app"}`)
	b := []byte(`{"_id":"bbb","e2e":"x","source":"app"}`)
	if !mongoPayloadsEqual(a, b) {
		t.Fatal("expected equal when only _id differs")
	}
}

func TestMongoPayloadsEqual_detectsValueDiff(t *testing.T) {
	a := []byte(`{"_id":"aaa","e2e":"x"}`)
	b := []byte(`{"_id":"bbb","e2e":"y"}`)
	if mongoPayloadsEqual(a, b) {
		t.Fatal("expected mismatch on e2e field")
	}
}
