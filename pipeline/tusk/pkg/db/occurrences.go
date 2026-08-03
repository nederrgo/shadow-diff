package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// ponytail: hard cap so a chatty signature cannot blow the ShadowDiff response.
// Upgrade path: ?offset=&limit= pagination on GET /api/v1/diffs/occurrences.
const maxOccurrences = 50

// SignatureOccurrence is one aligned index across control-a / control-b / candidate.
type SignatureOccurrence struct {
	Index            int             `json:"index"`
	ControlAPayload  json.RawMessage `json:"control_a_payload"`
	ControlBPayload  json.RawMessage `json:"control_b_payload"`
	CandidatePayload json.RawMessage `json:"candidate_payload"`
}

// SignatureOccurrences is the lazy pager payload for one (trace_id, signature, execution).
type SignatureOccurrences struct {
	TraceID           string                `json:"trace_id"`
	Signature         string                `json:"signature"`
	ReplayExecutionID string                `json:"replay_execution_id,omitempty"`
	Occurrences       []SignatureOccurrence `json:"occurrences"`
	Truncated         bool                  `json:"truncated"`
}

// rawRolePayload is one raw_reports row used while aligning roles.
type rawRolePayload struct {
	Role    string
	Payload []byte
}

// GetSignatureOccurrences loads raw_reports for a signature and aligns roles by
// capture order (same bucketing as Beru compareSignature).
// When execID is empty, the latest execution that contains this trace is used.
func (s *Store) GetSignatureOccurrences(ctx context.Context, traceID, signature, execID string) (SignatureOccurrences, error) {
	out := SignatureOccurrences{
		TraceID:     traceID,
		Signature:   signature,
		Occurrences: []SignatureOccurrence{},
	}
	if execID == "" {
		var err error
		execID, err = s.latestExecutionForTrace(ctx, traceID)
		if err != nil {
			return out, err
		}
	}
	out.ReplayExecutionID = execID
	if execID == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT shadow_role, payload_bytes
FROM raw_reports
WHERE replay_execution_id = $1 AND trace_id = $2 AND signature = $3
ORDER BY captured_at ASC, id ASC`, execID, traceID, signature)
	if err != nil {
		return out, fmt.Errorf("get signature occurrences: %w", err)
	}
	defer rows.Close()

	var raw []rawRolePayload
	for rows.Next() {
		var role string
		var payload []byte
		if err := rows.Scan(&role, &payload); err != nil {
			return out, fmt.Errorf("get signature occurrences scan: %w", err)
		}
		raw = append(raw, rawRolePayload{Role: role, Payload: payload})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	out.Occurrences, out.Truncated = alignOccurrences(raw, maxOccurrences)
	return out, nil
}

func (s *Store) latestExecutionForTrace(ctx context.Context, traceID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT replay_execution_id
FROM raw_reports
WHERE trace_id = $1
ORDER BY captured_at DESC, id DESC
LIMIT 1`, traceID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest execution for trace: %w", err)
	}
	return id, nil
}

// alignOccurrences groups per-role ordered payloads and zips by index.
func alignOccurrences(rows []rawRolePayload, limit int) ([]SignatureOccurrence, bool) {
	var a, b, c []json.RawMessage
	for _, r := range rows {
		p := decodePayloadBytes(r.Payload)
		switch r.Role {
		case "control-a":
			a = append(a, p)
		case "control-b":
			b = append(b, p)
		case "candidate":
			c = append(c, p)
		}
	}
	n := max(len(a), len(b), len(c))
	truncated := false
	if limit > 0 && n > limit {
		n = limit
		truncated = true
	}
	out := make([]SignatureOccurrence, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, SignatureOccurrence{
			Index:            i,
			ControlAPayload:  atOrNil(a, i),
			ControlBPayload:  atOrNil(b, i),
			CandidatePayload: atOrNil(c, i),
		})
	}
	return out, truncated
}

func atOrNil(slice []json.RawMessage, i int) json.RawMessage {
	if i >= len(slice) {
		return nil
	}
	return slice[i]
}

// decodePayloadBytes mirrors Beru payloadJSON: valid JSON as-is, else {"_raw":…}.
func decodePayloadBytes(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	wrapped, err := json.Marshal(map[string]string{"_raw": string(b)})
	if err != nil {
		return nil
	}
	return json.RawMessage(wrapped)
}
