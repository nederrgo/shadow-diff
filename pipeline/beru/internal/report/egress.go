package report

import (
	"fmt"
	"time"

	"github.com/shadow-diff/beru/internal/model"
)

func FromEgress(traceID, role, protocol, shadowTestName string, payload []byte) (*model.RawReport, error) {
	return FromEgressWithSignature(traceID, role, protocol, shadowTestName, "", payload)
}

// FromEgressWithSignature builds an egress report, using the caller's signature
// when one is supplied and deriving it from the payload otherwise.
//
// A producer that already decoded the protocol knows its own
// `protocol:operation:target` exactly. Deriving it here instead means guessing
// from payload shape — for a database payload that means picking the first
// non-`$` string key in sorted order, which stops being the operation as soon as
// the payload carries any other top-level string. Since the signature is what
// buckets a trace's reports for comparison, letting it drift produces phantom
// MISMATCH_SIGNATURE verdicts rather than an obvious failure.
func FromEgressWithSignature(traceID, role, protocol, shadowTestName, signature string, payload []byte) (*model.RawReport, error) {
	if traceID == "" || role == "" || protocol == "" {
		return nil, fmt.Errorf("incomplete egress report")
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty egress payload")
	}
	if signature == "" {
		signature = EgressSignature(protocol, payload)
	}
	return &model.RawReport{
		TraceID:        traceID,
		ShadowRole:     role,
		ShadowTestName: shadowTestName,
		Protocol:       protocol,
		Direction:      model.DirectionEgress,
		Signature:      signature,
		PayloadBytes:   append([]byte(nil), payload...),
		CapturedAt:     time.Now().UTC(),
	}, nil
}
