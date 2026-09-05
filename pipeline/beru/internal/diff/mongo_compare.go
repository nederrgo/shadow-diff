package diff

import (
	"bytes"
	"encoding/json"
)

// mongoPayloadsEqual compares Mongo egress payloads ignoring per-connection metadata.
// Fields stripped: _id (auto-generated doc ID), lsid (session ID unique per connection),
// comment (traceparent injected by Shadow-Diff), $db (database routing metadata).
func mongoPayloadsEqual(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	na, errA := stripMongoMetadataFields(a)
	nb, errB := stripMongoMetadataFields(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(na, nb)
}

// mongoMetadataFields are per-connection or per-request fields that vary across
// shadow roles even for semantically identical MongoDB operations.
var mongoMetadataFields = []string{"_id", "lsid", "comment", "$db"}

func stripMongoMetadataFields(payload []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, err
	}
	for _, field := range mongoMetadataFields {
		delete(obj, field)
	}
	return json.Marshal(obj)
}
