package report

import (
	"fmt"
	"time"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

func FromEgress(traceID, role, protocol, shadowTestName string, payload []byte) (*storage.RawReport, error) {
	if traceID == "" || role == "" || protocol == "" {
		return nil, fmt.Errorf("incomplete egress report")
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty egress payload")
	}
	return &storage.RawReport{
		TraceID:        traceID,
		ShadowRole:     role,
		ShadowTestName: shadowTestName,
		Protocol:       protocol,
		Direction:      storage.DirectionEgress,
		Signature:      EgressSignature(protocol, payload),
		PayloadBytes:   append([]byte(nil), payload...),
		CapturedAt:     time.Now().UTC(),
	}, nil
}

func FromMongoEgress(traceID, role, shadowTestName string, payload []byte, hints MongoHints) (*storage.RawReport, error) {
	if traceID == "" || role == "" {
		return nil, fmt.Errorf("incomplete egress report")
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	return &storage.RawReport{
		TraceID:        traceID,
		ShadowRole:     role,
		ShadowTestName: shadowTestName,
		Protocol:       "mongodb",
		Direction:      storage.DirectionEgress,
		Signature:      MongoSignature(payload, hints),
		PayloadBytes:   append([]byte(nil), payload...),
		CapturedAt:     time.Now().UTC(),
	}, nil
}
