package storage

import "time"

type PayloadDirection string

const (
	DirectionIngress PayloadDirection = "ingress"
	DirectionEgress  PayloadDirection = "egress"
)

type RawReport struct {
	TraceID        string           `json:"trace_id"`
	ShadowRole     string           `json:"shadow_role"` // control-a, control-b, candidate
	ShadowTestName string           `json:"shadow_test_name"`
	Protocol       string           `json:"protocol"` // http, mongodb, rabbitmq, kafka
	Direction      PayloadDirection   `json:"direction"`
	Signature      string           `json:"signature"` // e.g., "mongodb:insert:orders"
	PayloadBytes   []byte           `json:"payload_bytes"`
	CapturedAt     time.Time        `json:"captured_at"`
}

// TraceSummary is one dashboard row: a trace plus protocol with diff status.
type TraceSummary struct {
	TraceID        string           `json:"trace_id"`
	Protocol       string           `json:"protocol"`
	Direction      PayloadDirection `json:"direction,omitempty"` // set for http ingress vs egress rows
	ShadowTestName string           `json:"shadow_test_name"`
	LastCapturedAt string           `json:"last_captured_at"`
	Status         string           `json:"status"`
	Signatures     string           `json:"signatures"`
}

type VerdictState struct {
	Status             string // MATCH, MISMATCH, TIMEOUT
	HasCountRegression bool   // Explicit flag for N+1 anomalies
	SummaryDetails     string // Details explaining path errors
	UpdatedAt          time.Time
}
