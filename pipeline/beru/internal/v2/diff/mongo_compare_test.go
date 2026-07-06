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

func TestMongoPayloadsEqual_ignoresLsidAndComment(t *testing.T) {
	// Pixie req_body (command doc only): lsid and comment differ per role, insert and ordered are the same.
	a := []byte(`{"insert":"orders","ordered":true,"lsid":{"id":{"$binary":{"base64":"abc=","subType":"04"}}},"comment":"00-aaaa-bbbb-01","$db":"test"}`)
	b := []byte(`{"insert":"orders","ordered":true,"lsid":{"id":{"$binary":{"base64":"xyz=","subType":"04"}}},"comment":"00-cccc-dddd-01","$db":"test"}`)
	if !mongoPayloadsEqual(a, b) {
		t.Fatal("expected equal when only lsid and comment differ")
	}
}

func TestMongoPayloadsEqual_detectsDifferentCollection(t *testing.T) {
	a := []byte(`{"insert":"orders","ordered":true,"lsid":{"id":"abc"},"comment":"tp","$db":"test"}`)
	b := []byte(`{"insert":"shipments","ordered":true,"lsid":{"id":"xyz"},"comment":"tp","$db":"test"}`)
	if mongoPayloadsEqual(a, b) {
		t.Fatal("expected mismatch on collection name")
	}
}
