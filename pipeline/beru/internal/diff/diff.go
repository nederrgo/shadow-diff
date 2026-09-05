package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shadow-diff/beru/internal/model"
)

const (
	roleControlA  = "control-a"
	roleControlB  = "control-b"
	roleCandidate = "candidate"

	defaultTraceTimeout = 10 * time.Second
)

// EvalOptions controls completeness timeout behaviour.
type EvalOptions struct {
	Timeout time.Duration // zero → 10s
	Now     time.Time     // zero → time.Now().UTC()
}

// sigBuckets maps signature -> chronologically ordered reports for one shadow role.
type sigBuckets map[string][]model.RawReport

// roleMap maps shadow role -> signature buckets within one protocol.
type roleMap map[string]sigBuckets

// protoMap maps protocol -> role buckets for a trace timeline.
type protoMap map[string]roleMap

// EvaluateTraceHistory runs the strict 3-step verdict pipeline.
// Returns nil when the trace is still waiting for roles (within the timeout window).
func EvaluateTraceHistory(history []model.RawReport, userNoise map[string]struct{}, opts EvalOptions) *model.VerdictState {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTraceTimeout
	}

	verdict := &model.VerdictState{
		Status:    model.StatusMatch,
		UpdatedAt: now,
	}
	if len(history) == 0 {
		return verdict
	}

	// Step 1: Trace completeness.
	roles := rolesPresent(history)
	missing := missingRoles(roles)
	if len(missing) > 0 {
		first := earliestCaptured(history)
		if now.Sub(first) >= timeout {
			details := model.VerdictDetails{Missing: missing}
			verdict.Status = model.StatusWaitingForRoles
			verdict.SummaryDetails = mustJSON(details)
			return verdict
		}
		return nil
	}

	// Group for baseline + candidate evaluation.
	grouped := groupHistory(history)
	chronological := chronologicalByRole(history)

	// Step 2: Baseline verification (control-a vs control-b).
	if fail := verifyBaseline(grouped, chronological); fail != nil {
		details := model.VerdictDetails{Baseline: fail}
		verdict.Status = model.StatusVoidedBaselineDivergence
		verdict.SummaryDetails = mustJSON(details)
		return verdict
	}

	// Step 3: Compound candidate evaluation against control-a.
	var steps []model.VerdictStep
	flagSet := make(map[string]struct{})

	// HTTP ingress status: candidate vs control-a.
	steps = append(steps, compareIngressStatus(chronological, flagSet)...)

	for _, protocol := range sortedKeys(grouped) {
		rm := grouped[protocol]
		controlA := rm[roleControlA]
		controlB := rm[roleControlB]
		candidate := rm[roleCandidate]
		for _, signature := range unionSignatures(controlA, candidate) {
			aSlice := controlA[signature]
			bSlice := controlB[signature]
			cSlice := candidate[signature]
			steps = append(steps, compareSignature(protocol, signature, aSlice, bSlice, cSlice, flagSet, userNoise)...)
		}
	}

	if len(steps) > 0 {
		flags := sortedKeys(flagSet)
		verdict.Status = model.StatusMismatch
		verdict.Flags = flags
		verdict.HasCountRegression = hasFlag(flagSet, model.FlagMismatchCount) || hasFlag(flagSet, model.FlagMismatchSignature)
		verdict.SummaryDetails = mustJSON(model.VerdictDetails{Flags: flags, Steps: steps})
	}
	return verdict
}

func rolesPresent(history []model.RawReport) map[string]struct{} {
	have := make(map[string]struct{})
	for _, r := range history {
		have[r.ShadowRole] = struct{}{}
	}
	return have
}

func missingRoles(have map[string]struct{}) []string {
	var missing []string
	for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
		if _, ok := have[role]; !ok {
			missing = append(missing, role)
		}
	}
	return missing
}

func earliestCaptured(history []model.RawReport) time.Time {
	first := history[0].CapturedAt
	for _, r := range history[1:] {
		if r.CapturedAt.Before(first) {
			first = r.CapturedAt
		}
	}
	return first
}

func groupHistory(history []model.RawReport) protoMap {
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
	return grouped
}

// chronologicalByRole returns protocol -> role -> reports in capture order.
func chronologicalByRole(history []model.RawReport) map[string]map[string][]model.RawReport {
	out := make(map[string]map[string][]model.RawReport)
	for _, report := range history {
		if out[report.Protocol] == nil {
			out[report.Protocol] = make(map[string][]model.RawReport)
		}
		out[report.Protocol][report.ShadowRole] = append(out[report.Protocol][report.ShadowRole], report)
	}
	return out
}

func verifyBaseline(grouped protoMap, chronological map[string]map[string][]model.RawReport) *model.BaselineFailure {
	// HTTP ingress status codes must match between control-a and control-b.
	for protocol, byRole := range chronological {
		if !strings.EqualFold(protocol, "http") {
			continue
		}
		aReports := filterDirection(byRole[roleControlA], model.DirectionIngress)
		bReports := filterDirection(byRole[roleControlB], model.DirectionIngress)
		n := min(len(aReports), len(bReports))
		for i := 0; i < n; i++ {
			aCode, bCode := aReports[i].StatusCode, bReports[i].StatusCode
			if aCode != bCode {
				return &model.BaselineFailure{
					Reason:   "status_code_mismatch",
					Protocol: protocol,
					Detail:   fmt.Sprintf("ingress status control-a=%s control-b=%s index=%d", aCode, bCode, i),
					ControlA: aCode,
					ControlB: bCode,
				}
			}
		}
		if len(aReports) != len(bReports) {
			return &model.BaselineFailure{
				Reason:   "ingress_count_mismatch",
				Protocol: protocol,
				Detail:   fmt.Sprintf("ingress count control-a=%d control-b=%d", len(aReports), len(bReports)),
				ControlA: fmt.Sprintf("%d", len(aReports)),
				ControlB: fmt.Sprintf("%d", len(bReports)),
			}
		}
	}

	// Per-signature egress counts (same buckets as candidate compare; order ignored).
	for _, protocol := range sortedKeys(grouped) {
		aBuckets := grouped[protocol][roleControlA]
		bBuckets := grouped[protocol][roleControlB]
		for _, sig := range unionSignatures(aBuckets, bBuckets) {
			nA := countEgress(aBuckets[sig])
			nB := countEgress(bBuckets[sig])
			if nA != nB {
				return &model.BaselineFailure{
					Reason:   "egress_count_mismatch",
					Protocol: protocol,
					Detail:   fmt.Sprintf("signature=%s egress count control-a=%d control-b=%d", sig, nA, nB),
					ControlA: fmt.Sprintf("%d", nA),
					ControlB: fmt.Sprintf("%d", nB),
				}
			}
		}
	}
	return nil
}

func filterDirection(reports []model.RawReport, dir model.PayloadDirection) []model.RawReport {
	var out []model.RawReport
	for _, r := range reports {
		if effectiveDirection(r) == dir {
			out = append(out, r)
		}
	}
	return out
}

func effectiveDirection(r model.RawReport) model.PayloadDirection {
	if r.Direction != "" {
		return r.Direction
	}
	// Unspecified: http defaults to ingress; everything else is egress.
	if strings.EqualFold(r.Protocol, "http") {
		return model.DirectionIngress
	}
	return model.DirectionEgress
}

func countEgress(reports []model.RawReport) int {
	n := 0
	for _, r := range reports {
		if effectiveDirection(r) == model.DirectionEgress {
			n++
		}
	}
	return n
}

func compareIngressStatus(chronological map[string]map[string][]model.RawReport, flagSet map[string]struct{}) []model.VerdictStep {
	var steps []model.VerdictStep
	for protocol, byRole := range chronological {
		if !strings.EqualFold(protocol, "http") {
			continue
		}
		aReports := filterDirection(byRole[roleControlA], model.DirectionIngress)
		cReports := filterDirection(byRole[roleCandidate], model.DirectionIngress)
		n := min(len(aReports), len(cReports))
		for i := 0; i < n; i++ {
			if aReports[i].StatusCode == cReports[i].StatusCode {
				continue
			}
			idx := i
			flagSet[model.FlagMismatchPayload] = struct{}{}
			steps = append(steps, model.VerdictStep{
				Kind:      model.FlagMismatchPayload,
				Reason:    model.ReasonStatusCode,
				Protocol:  protocol,
				Signature: aReports[i].Signature,
				Index:     &idx,
				Detail:    fmt.Sprintf("status control-a=%s candidate=%s", aReports[i].StatusCode, cReports[i].StatusCode),
			})
		}
	}
	return steps
}

func compareSignature(protocol, signature string, aSlice, bSlice, cSlice []model.RawReport, flagSet map[string]struct{}, userNoise map[string]struct{}) []model.VerdictStep {
	var steps []model.VerdictStep
	nA, nB, nC := len(aSlice), len(bSlice), len(cSlice)

	// Candidate-only signature (not present on control-a).
	if nA == 0 && nC > 0 {
		flagSet[model.FlagMismatchSignature] = struct{}{}
		steps = append(steps, model.VerdictStep{
			Kind:      model.FlagMismatchSignature,
			Reason:    model.ReasonUnexpectedExtraEgress,
			Protocol:  protocol,
			Signature: signature,
			Detail:    fmt.Sprintf("unexpected signature candidate=%d", nC),
		})
		return steps
	}

	if nC > nA {
		flagSet[model.FlagMismatchCount] = struct{}{}
		steps = append(steps, model.VerdictStep{
			Kind:      model.FlagMismatchCount,
			Reason:    model.ReasonUnexpectedExtraEgress,
			Protocol:  protocol,
			Signature: signature,
			Detail:    fmt.Sprintf("candidate=%d control-a=%d", nC, nA),
		})
	} else if nC < nA {
		flagSet[model.FlagMismatchCount] = struct{}{}
		steps = append(steps, model.VerdictStep{
			Kind:      model.FlagMismatchCount,
			Reason:    model.ReasonMissingEgress,
			Protocol:  protocol,
			Signature: signature,
			Detail:    fmt.Sprintf("candidate=%d control-a=%d", nC, nA),
		})
	}

	pairCount := min(nA, nC)
	for i := 0; i < pairCount; i++ {
		aP, cP := aSlice[i].PayloadBytes, cSlice[i].PayloadBytes
		if payloadsEqual(protocol, aP, cP) {
			continue
		}
		var bP []byte
		if i < nB {
			bP = bSlice[i].PayloadBytes
		}
		fields, regressed := payloadRegressions(aP, bP, cP, userNoise)
		if !regressed {
			continue
		}
		idx := i
		flagSet[model.FlagMismatchPayload] = struct{}{}
		if len(fields) == 0 {
			// Non-JSON body mismatch: show the step, but no Ignore-path (not a field filter).
			steps = append(steps, model.VerdictStep{
				Kind:      model.FlagMismatchPayload,
				Protocol:  protocol,
				Signature: signature,
				Index:     &idx,
				Detail:    fmt.Sprintf("payload mismatch index=%d", i),
			})
			continue
		}
		for _, field := range fields {
			steps = append(steps, model.VerdictStep{
				Kind:      model.FlagMismatchPayload,
				Protocol:  protocol,
				Signature: signature,
				Index:     &idx,
				Detail:    fmt.Sprintf("payload mismatch index=%d field=%s", i, field),
				NoisePath: field,
			})
		}
	}
	return steps
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

func hasFlag(set map[string]struct{}, flag string) bool {
	_, ok := set[flag]
	return ok
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// hasPayloadRegression returns true when A→C contains at least one field difference
// not explained by A/B natural noise or user-configured noise paths.
func hasPayloadRegression(aP, bP, cP []byte, userNoise map[string]struct{}) bool {
	_, ok := payloadRegressions(aP, bP, cP, userNoise)
	return ok
}

// payloadRegressions returns filterable JSON leaf labels and whether a regression exists.
// Non-JSON body inequality returns (nil, true) — mismatch without a noise-filter path.
func payloadRegressions(aP, bP, cP []byte, userNoise map[string]struct{}) (fields []string, regressed bool) {
	abNoise, abErr := jsonNoisePaths(aP, bP)
	if abErr != nil {
		if bytes.Equal(aP, bP) && !bytes.Equal(aP, cP) {
			return nil, true
		}
		return nil, false
	}
	regs, err := jsonRegressions(aP, cP, mergeNoise(abNoise, userNoise))
	if err != nil {
		return nil, !bytes.Equal(aP, cP)
	}
	if len(regs) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(regs))
	seen := make(map[string]struct{}, len(regs))
	for _, d := range regs {
		if _, ok := seen[d.path]; ok {
			continue
		}
		seen[d.path] = struct{}{}
		out = append(out, d.path)
	}
	return out, true
}

type pathDiff struct{ path, expected, actual string }

func compareJSON(a, b []byte) ([]pathDiff, error) {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return nil, err
	}
	var out []pathDiff
	walkCompare("", va, vb, &out)
	return out, nil
}

func walkCompare(prefix string, a, b any, out *[]pathDiff) {
	if jsonEqual(a, b) {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, pathDiff{path: fieldLabel(prefix), expected: fmt.Sprintf("%v", a), actual: fmt.Sprintf("%v", b)})
			return
		}
		keys := make(map[string]struct{})
		for k := range av {
			keys[k] = struct{}{}
		}
		for k := range bv {
			keys[k] = struct{}{}
		}
		for _, k := range sortedKeys(keys) {
			walkCompare(joinPath(prefix, k), av[k], bv[k], out)
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, pathDiff{path: fieldLabel(prefix), expected: fmt.Sprintf("%v", a), actual: fmt.Sprintf("%v", b)})
			return
		}
		n := len(av)
		if len(bv) > n {
			n = len(bv)
		}
		for i := 0; i < n; i++ {
			var ai, bi any
			if i < len(av) {
				ai = av[i]
			}
			if i < len(bv) {
				bi = bv[i]
			}
			walkCompare(fmt.Sprintf("%s[%d]", prefix, i), ai, bi, out)
		}
	default:
		*out = append(*out, pathDiff{path: fieldLabel(prefix), expected: fmt.Sprintf("%v", a), actual: fmt.Sprintf("%v", b)})
	}
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func fieldLabel(path string) string {
	if path == "" {
		return "(root)"
	}
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// jsonNoisePaths returns JSON paths that differ between a and b (natural A/B noise).
// Returns nil,nil when b is empty (no control-b available yet).
func jsonNoisePaths(a, b []byte) (map[string]struct{}, error) {
	if len(b) == 0 {
		return nil, nil
	}
	diffs, err := compareJSON(a, b)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(diffs))
	for _, d := range diffs {
		out[d.path] = struct{}{}
	}
	return out, nil
}

// jsonRegressions returns diffs between a and c excluding any paths in noise.
func jsonRegressions(a, c []byte, noise map[string]struct{}) ([]pathDiff, error) {
	diffs, err := compareJSON(a, c)
	if err != nil {
		return nil, err
	}
	var out []pathDiff
	for _, d := range diffs {
		if _, skip := noise[d.path]; skip {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func mergeNoise(abNoise, userNoise map[string]struct{}) map[string]struct{} {
	if len(abNoise) == 0 && len(userNoise) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(abNoise)+len(userNoise))
	for p := range abNoise {
		out[p] = struct{}{}
	}
	for p := range userNoise {
		out[p] = struct{}{}
	}
	return out
}
