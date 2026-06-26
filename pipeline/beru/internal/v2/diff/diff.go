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
		roleMap := grouped[protocol]
		controlA := roleMap[roleControlA]
		candidate := roleMap[roleCandidate]
		for _, signature := range unionSignatures(controlA, candidate) {
			aSlice := controlA[signature]
			cSlice := candidate[signature]
			details = append(details, compareSignature(protocol, signature, aSlice, cSlice, verdict)...)
		}
	}

	if len(details) > 0 {
		verdict.Status = statusMismatch
		verdict.SummaryDetails = strings.Join(details, "; ")
	}
	return verdict
}

func compareSignature(protocol, signature string, aSlice, cSlice []storage.RawReport, verdict *storage.VerdictState) []string {
	label := protocol + ":" + signature
	var details []string

	if len(cSlice) > len(aSlice) {
		verdict.HasCountRegression = true
		details = append(details, fmt.Sprintf(
			"count regression: %s candidate=%d control-a=%d",
			label, len(cSlice), len(aSlice),
		))
	} else if len(cSlice) < len(aSlice) {
		details = append(details, fmt.Sprintf(
			"count deficit: %s candidate=%d control-a=%d",
			label, len(cSlice), len(aSlice),
		))
	}

	pairCount := len(aSlice)
	if len(cSlice) < pairCount {
		pairCount = len(cSlice)
	}
	for i := 0; i < pairCount; i++ {
		if bytes.Equal(aSlice[i].PayloadBytes, cSlice[i].PayloadBytes) {
			continue
		}
		details = append(details, fmt.Sprintf("payload mismatch: %s index=%d", label, i))
	}
	return details
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
