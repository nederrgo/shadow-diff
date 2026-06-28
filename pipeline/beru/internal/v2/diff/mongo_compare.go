package diff

import (
	"bytes"
	"encoding/json"
)

// mongoPayloadsEqual compares Mongo egress payloads, ignoring auto-generated _id fields
// that differ across roles when enhancedDatabaseReporting captures full documents.
func mongoPayloadsEqual(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	na, errA := stripMongoIDField(a)
	nb, errB := stripMongoIDField(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(na, nb)
}

func stripMongoIDField(payload []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, err
	}
	delete(obj, "_id")
	return json.Marshal(obj)
}
