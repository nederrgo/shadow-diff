package diff

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

const (
	statusMatch    = "MATCH"
	statusMismatch = "MISMATCH"

	roleControlA  = "control-a"
	roleControlB  = "control-b"
	roleCandidate = "candidate"
)

// sigBuckets maps signature -> chronologically ordered reports for one shadow role.
type sigBuckets map[string][]storage.RawReport

// roleMap maps shadow role -> signature buckets within one protocol.
type roleMap map[string]sigBuckets

// protoMap maps protocol -> role buckets for a trace timeline.
type protoMap map[string]roleMap

func EvaluateTraceHistory(history []storage.RawReport) *storage.VerdictState {
	verdict := &storage.VerdictState{
		Status:    statusMatch,
		UpdatedAt: time.Now().UTC(),
	}
	if len(history) == 0 {
		return verdict
	}

	grouped := make(protoMap)
	for _, report := range history {
		if grouped[report.Protocol] == nil {
			grouped[report.Protocol] = make(roleMap)
		}
		if grouped[report.Protocol][report.ShadowRole] == nil {
			grouped[report.Protocol][report.ShadowRole] = make(sigBuckets)
		}
		sig := report.Signature
		grouped[report.Protocol][report.ShadowRole][sig] = append(
			grouped[report.Protocol][report.ShadowRole][sig],
			report,
		)
	}

	var details []string
	for _, protocol := range sortedKeys(grouped) {
		rm := grouped[protocol]
		controlA := rm[roleControlA]
		controlB := rm[roleControlB] // may be nil if not yet arrived
		candidate := rm[roleCandidate]
		for _, signature := range unionSignatures(controlA, candidate) {
			aSlice := controlA[signature]
			bSlice := controlB[signature] // nil → noise cancellation skipped
			cSlice := candidate[signature]
			details = append(details, compareSignature(protocol, signature, aSlice, bSlice, cSlice, verdict)...)
		}
	}

	if len(details) > 0 {
		verdict.Status = statusMismatch
		verdict.SummaryDetails = strings.Join(details, "; ")
	}
	return verdict
}

func compareSignature(protocol, signature string, aSlice, bSlice, cSlice []storage.RawReport, verdict *storage.VerdictState) []string {
	label := protocol + ":" + signature
	var details []string

	// Count diff-of-diffs: candidate is only a count regression when it exceeds the noise
	// band established by the delta between the two control replicas.
	nA, nB, nC := len(aSlice), len(bSlice), len(cSlice)
	noiseCountDelta := 0
	if nB > 0 {
		noiseCountDelta = nB - nA
	}
	candidateCountDelta := nC - nA
	if candidateCountDelta > max(0, noiseCountDelta) {
		verdict.HasCountRegression = true
		details = append(details, fmt.Sprintf(
			"count regression: %s candidate=%d control-a=%d",
			label, nC, nA,
		))
	} else if nC < nA {
		details = append(details, fmt.Sprintf(
			"count deficit: %s candidate=%d control-a=%d",
			label, nC, nA,
		))
	}

	// Payload diff-of-diffs: a mismatch between control-a and candidate is a real
	// regression only when the same position is NOT already noisy in control-b.
	pairCount := min(nA, nC)
	for i := 0; i < pairCount; i++ {
		if payloadsEqual(protocol, aSlice[i].PayloadBytes, cSlice[i].PayloadBytes) {
			continue
		}
		// If control-b exists at this index and already disagrees with control-a,
		// the field is non-deterministic — not a candidate regression.
		if i < nB && !payloadsEqual(protocol, aSlice[i].PayloadBytes, bSlice[i].PayloadBytes) {
			continue
		}
		details = append(details, fmt.Sprintf("payload mismatch: %s index=%d", label, i))
	}
	return details
}

func payloadsEqual(protocol string, a, b []byte) bool {
	if strings.EqualFold(protocol, "mongodb") {
		return mongoPayloadsEqual(a, b)
	}
	return bytes.Equal(a, b)
}

func unionSignatures(a, b sigBuckets) []string {
	seen := make(map[string]struct{})
	for sig := range a {
		seen[sig] = struct{}{}
	}
	for sig := range b {
		seen[sig] = struct{}{}
	}
	return sortedKeys(seen)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
