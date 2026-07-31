package storage

import "time"

type PayloadDirection string

const (
	DirectionIngress PayloadDirection = "ingress"
	DirectionEgress  PayloadDirection = "egress"
)

// Verdict status constants.
const (
	StatusMatch                    = "MATCH"
	StatusMismatch                 = "MISMATCH"
	StatusVoidedBaselineDivergence = "VOIDED_BASELINE_DIVERGENCE"
	StatusWaitingForRoles          = "WAITING_FOR_ROLES"
)

// Mismatch detail flag / step kinds.
const (
	FlagMismatchPayload   = "MISMATCH_PAYLOAD"
	FlagMismatchCount     = "MISMATCH_COUNT"
	FlagMismatchSignature = "MISMATCH_SIGNATURE"
)

// Step reason constants.
const (
	ReasonUnexpectedExtraEgress = "UNEXPECTED_EXTRA_EGRESS"
	ReasonMissingEgress         = "MISSING_EGRESS"
	ReasonStatusCode            = "STATUS_CODE"
)

type RawReport struct {
	TraceID        string           `json:"trace_id"`
	ShadowRole     string           `json:"shadow_role"` // control-a, control-b, candidate
	ShadowTestName string           `json:"shadow_test_name"`
	Protocol       string           `json:"protocol"` // http, mongodb, rabbitmq, kafka
	Direction      PayloadDirection `json:"direction"`
	Signature      string           `json:"signature"` // e.g., "mongodb:insert:orders"
	StatusCode     string           `json:"status_code,omitempty"`
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
	Status             string    `json:"status"` // MATCH | MISMATCH | VOIDED_BASELINE_DIVERGENCE | WAITING_FOR_ROLES
	HasCountRegression bool      `json:"has_count_regression"`
	Flags              []string  `json:"flags,omitempty"` // MISMATCH_PAYLOAD | MISMATCH_COUNT | MISMATCH_SIGNATURE
	SummaryDetails     string    `json:"summary_details"` // JSON blob (VerdictDetails)
	UpdatedAt          time.Time `json:"updated_at"`
}

// VerdictDetails is the JSON shape stored in VerdictState.SummaryDetails.
type VerdictDetails struct {
	Flags    []string          `json:"flags,omitempty"`
	Steps    []VerdictStep     `json:"steps,omitempty"`
	Baseline *BaselineFailure  `json:"baseline,omitempty"`
	Missing  []string          `json:"missing_roles,omitempty"`
}

// VerdictStep is one compound-diff finding for a MISMATCH verdict.
type VerdictStep struct {
	Kind      string `json:"kind"` // MISMATCH_PAYLOAD | MISMATCH_COUNT | MISMATCH_SIGNATURE
	Reason    string `json:"reason,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	Signature string `json:"signature,omitempty"`
	Index     *int   `json:"index,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// NoisePath is a JSON leaf field name eligible for noise_filters (payload diffs only).
	NoisePath string `json:"noise_path,omitempty"`
}

// BaselineFailure describes why control-a vs control-b voided the trace.
type BaselineFailure struct {
	Reason    string `json:"reason"`
	Protocol  string `json:"protocol,omitempty"`
	Detail    string `json:"detail,omitempty"`
	ControlA  string `json:"control_a,omitempty"`
	ControlB  string `json:"control_b,omitempty"`
}

// TraceGroup is one (trace_id, protocol) pair for dashboard listing.
type TraceGroup struct {
	TraceID        string
	Protocol       string
	LastCapturedAt string
}

// StaleIncompleteTrace is a reaper candidate: incomplete roles past the timeout.
type StaleIncompleteTrace struct {
	TraceID   string
	FirstSeen time.Time
}
