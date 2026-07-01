package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/trace"
	"github.com/shadow-diff/beru/internal/v2/storage"
)

// NetworkEventEnvelope is the wire format posted by Envoy sidecars to beru-ingest.
type NetworkEventEnvelope struct {
	TraceID            string    `json:"trace_id"`
	PodRole            string    `json:"pod_role"`
	ShadowTestName     string    `json:"shadow_test_name,omitempty"`
	Protocol           string    `json:"protocol"`
	Direction          string    `json:"direction"`
	Timestamp          time.Time `json:"timestamp"`
	RawRequestPayload  string    `json:"raw_request"`
	RawResponsePayload string    `json:"raw_response"`
	Metadata           string    `json:"metadata,omitempty"`
}

type wireStoredPayload struct {
	Request  string `json:"request"`
	Response string `json:"response"`
	Metadata string `json:"metadata,omitempty"`
}

type httpWireMeta struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type mongoWireMeta struct {
	Command    string `json:"command"`
	Collection string `json:"collection"`
}

// FromWireEnvelope maps a network-level event into a RawReport for the state engine.
func FromWireEnvelope(env *NetworkEventEnvelope) (*storage.RawReport, error) {
	if env == nil {
		return nil, fmt.Errorf("nil envelope")
	}
	traceID, err := normalizeWireTraceID(env.TraceID)
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(env.PodRole)
	if !roles.IsValid(role) {
		return nil, fmt.Errorf("invalid pod_role %q", env.PodRole)
	}
	protocol := strings.ToLower(strings.TrimSpace(env.Protocol))
	if protocol == "" {
		return nil, fmt.Errorf("protocol is required")
	}

	direction := storage.DirectionEgress
	if strings.EqualFold(strings.TrimSpace(env.Direction), string(storage.DirectionIngress)) {
		direction = storage.DirectionIngress
	}

	stored, err := json.Marshal(wireStoredPayload{
		Request:  env.RawRequestPayload,
		Response: env.RawResponsePayload,
		Metadata: env.Metadata,
	})
	if err != nil {
		return nil, err
	}

	signature, err := wireSignature(protocol, env.Metadata, env.RawRequestPayload, stored)
	if err != nil {
		return nil, err
	}

	captured := env.Timestamp
	if captured.IsZero() {
		captured = time.Now().UTC()
	}

	return &storage.RawReport{
		TraceID:        traceID,
		ShadowRole:     role,
		ShadowTestName: strings.TrimSpace(env.ShadowTestName),
		Protocol:       protocol,
		Direction:      direction,
		Signature:      signature,
		PayloadBytes:   stored,
		CapturedAt:     captured,
	}, nil
}

func normalizeWireTraceID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("trace_id is required")
	}
	if tid, ok := trace.ParseTraceparent(raw); ok {
		return tid, nil
	}
	if len(raw) == 32 && isHexTraceID(raw) {
		return strings.ToLower(raw), nil
	}
	return "", fmt.Errorf("invalid trace_id %q", raw)
}

func isHexTraceID(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

func wireSignature(protocol, metadata, rawRequest string, stored []byte) (string, error) {
	switch protocol {
	case "http":
		method, path := parseHTTPWireMetadata(metadata, rawRequest)
		return HTTPSignature(method, path), nil
	case "mongodb":
		hints := parseMongoWireMetadata(metadata, rawRequest)
		reqPayload := []byte(rawRequest)
		if len(reqPayload) == 0 {
			reqPayload = stored
		}
		return MongoSignature(reqPayload, hints), nil
	default:
		return EgressSignature(protocol, stored), nil
	}
}

func parseHTTPWireMetadata(metadata, rawRequest string) (method, path string) {
	metadata = strings.TrimSpace(metadata)
	if metadata != "" {
		var m httpWireMeta
		if err := json.Unmarshal([]byte(metadata), &m); err == nil {
			if m.Method != "" || m.Path != "" {
				return m.Method, m.Path
			}
		}
	}
	line := strings.TrimSpace(strings.SplitN(rawRequest, "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return fields[0], fields[1]
	}
	return "", "/"
}

func parseMongoWireMetadata(metadata, rawRequest string) MongoHints {
	var hints MongoHints
	metadata = strings.TrimSpace(metadata)
	if metadata != "" {
		var m mongoWireMeta
		if err := json.Unmarshal([]byte(metadata), &m); err == nil {
			hints.Operation = m.Command
			hints.Collection = m.Collection
		}
	}
	if hints.Operation == "" && hints.Collection == "" && strings.TrimSpace(rawRequest) != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(rawRequest), &obj); err == nil {
			hints = completeMongoHints(hints, []byte(rawRequest))
		}
	}
	return hints
}
