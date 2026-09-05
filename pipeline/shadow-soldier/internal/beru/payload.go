// Package beru builds protocol-specific Beru payloads and queues reports.
package beru

import (
	"encoding/json"

	"github.com/shadow-diff/shadow-soldier/internal/parsers"
)

// BuildPayload renders a decoded query as the JSON body Beru stores and diffs.
//
// For MongoDB the command document is passed through as-is so its top-level keys
// survive — that is what lets Beru's mongoPayloadsEqual strip the per-connection
// noise (_id, lsid, comment, $db) before comparing. Every other protocol gets a
// small structured object.
func BuildPayload(q parsers.QueryReport, shadowPod string) (json.RawMessage, error) {
	if q.Protocol == parsers.ProtocolMongoDB && json.Valid([]byte(q.RawQuery)) {
		return json.RawMessage(q.RawQuery), nil
	}
	return json.Marshal(struct {
		Operation  string   `json:"operation"`
		Target     string   `json:"target"`
		RawQuery   string   `json:"raw_query"`
		Parameters []string `json:"parameters,omitempty"`
		ShadowPod  string   `json:"shadow_pod,omitempty"`
	}{
		Operation:  q.Operation,
		Target:     q.Target,
		RawQuery:   q.RawQuery,
		Parameters: q.Params,
		ShadowPod:  shadowPod,
	})
}
