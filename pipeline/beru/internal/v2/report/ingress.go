package report

import (
	"fmt"
	"time"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/payload"
	"github.com/shadow-diff/beru/internal/v2/storage"
)

var ingressCodec = payload.NewRegistry()

func FromTrafficReport(report *beruv1.TrafficReport, shadowTestName string) (*storage.RawReport, error) {
	if report == nil || report.TraceId == "" || report.Role == "" {
		return nil, fmt.Errorf("incomplete traffic report")
	}
	if report.Payload == nil {
		return nil, fmt.Errorf("empty payload")
	}
	meta := report.Payload.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	name := shadowTestName
	if meta["shadow_test_name"] != "" {
		name = meta["shadow_test_name"]
	}
	body, _, err := ingressCodec.Normalize(report.Payload.Body, meta, report.Payload.ContentType)
	if err != nil {
		return nil, err
	}
	method := meta[":method"]
	if method == "" {
		method = meta["method"]
	}
	path := meta[":path"]
	if path == "" {
		path = meta["path"]
	}
	return &storage.RawReport{
		TraceID:        report.TraceId,
		ShadowRole:     report.Role,
		ShadowTestName: name,
		Protocol:       "http",
		Direction:    storage.DirectionIngress,
		Signature:    HTTPSignature(method, path),
		PayloadBytes: body,
		CapturedAt:   time.Now().UTC(),
	}, nil
}

func FromHTTPIngress(traceID, role, shadowTestName, method, path string, meta map[string]string, body []byte, contentType string) (*storage.RawReport, error) {
	if traceID == "" || role == "" {
		return nil, fmt.Errorf("incomplete http ingress report")
	}
	if meta == nil {
		meta = map[string]string{}
	}
	normalized, _, err := ingressCodec.Normalize(body, meta, contentType)
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = meta[":method"]
	}
	if path == "" {
		path = meta[":path"]
	}
	name := shadowTestName
	if meta["shadow_test_name"] != "" {
		name = meta["shadow_test_name"]
	}
	return &storage.RawReport{
		TraceID:        traceID,
		ShadowRole:     role,
		ShadowTestName: name,
		Protocol:       "http",
		Direction:    storage.DirectionIngress,
		Signature:    HTTPSignature(method, path),
		PayloadBytes: normalized,
		CapturedAt:   time.Now().UTC(),
	}, nil
}
