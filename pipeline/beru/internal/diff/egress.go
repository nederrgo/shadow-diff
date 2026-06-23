package diff

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// AnalyzeEgress runs diff-of-diffs on ordered egress payload slices per protocol.
func AnalyzeEgress(
	log *slog.Logger,
	traceID, protocol string,
	controlA, controlB, candidate [][]byte,
	userNoise map[string]struct{},
) (Result, error) {
	if log == nil {
		log = slog.Default()
	}
	res := Result{
		TraceID:   traceID,
		Protocol:  protocol,
		ControlA:  copyByteSlices(controlA),
		ControlB:  copyByteSlices(controlB),
		Candidate: copyByteSlices(candidate),
	}
	if len(controlA) == 0 {
		log.Error("Could not run egress diff without control-a payload", "traceID", traceID, "protocol", protocol)
		res.Err = fmt.Errorf("missing control-a payload")
		return res, res.Err
	}
	if len(candidate) == 0 {
		log.Info(fmt.Sprintf("Timed out waiting for Trace %s (%s egress): missing candidate", traceID, protocol))
		res.Err = fmt.Errorf("missing candidate payload")
		return res, res.Err
	}
	res.BodyA = append([]byte(nil), controlA[0]...)
	res.BodyC = append([]byte(nil), candidate[0]...)

	if len(controlA) != len(candidate) {
		unit := egressCountUnit(protocol)
		log.Info(fmt.Sprintf(
			"Egress count regression for Trace %s (%s): expected %d %s but got %d",
			traceID, protocol, len(controlA), formatCountUnit(unit, len(controlA)), len(candidate),
		))
		res.Status = StatusMismatch
		res.Regressions = []PathDiff{{
			Path:     "(count)",
			Expected: fmt.Sprintf("%d", len(controlA)),
			Actual:   fmt.Sprintf("%d", len(candidate)),
		}}
		return res, nil
	}

	regs, err := pairAndDiffEgress(protocol, controlA, controlB, candidate, userNoise)
	if err != nil {
		log.Error("Could not compare control-a and candidate egress", "traceID", traceID, "protocol", protocol, "err", err)
		res.Err = err
		return res, err
	}
	res.Regressions = regs
	if len(regs) == 0 {
		res.Status = StatusMatch
		log.Info(fmt.Sprintf("No egress regression for Trace %s (%s)", traceID, protocol))
		return res, nil
	}
	res.Status = StatusMismatch
	ignored := formatIgnoredNoise(userNoise)
	for _, r := range regs {
		if strings.HasPrefix(r.Path, "(extra egress:") {
			log.Info(fmt.Sprintf(
				"Egress regression for Trace %s (%s): Unexpected extra egress operation (%s)",
				traceID, protocol, strings.TrimPrefix(strings.TrimSuffix(r.Path, ")"), "(extra egress: "),
			))
			continue
		}
		log.Info(fmt.Sprintf(
			"Egress regression for Trace %s (%s): Field '%s' expected %s but got %s%s",
			traceID, protocol, r.Path, r.Expected, r.Actual, ignored,
		))
	}
	return res, nil
}

// AnalyzeMongoEgress runs diff-of-diffs on MongoDB egress query payloads.
func AnalyzeMongoEgress(
	log *slog.Logger,
	traceID string,
	controlA, controlB, candidate [][]byte,
	userNoise map[string]struct{},
) (Result, error) {
	return AnalyzeEgress(log, traceID, "mongodb", controlA, controlB, candidate, userNoise)
}

func egressCountUnit(protocol string) string {
	switch strings.ToLower(protocol) {
	case "rabbitmq", "kafka":
		return "messages"
	case "mongodb", "postgresql", "redis":
		return "queries"
	default:
		return "operations"
	}
}

func formatCountUnit(unit string, count int) string {
	if count == 1 {
		switch unit {
		case "messages":
			return "message"
		case "queries":
			return "query"
		case "operations":
			return "operation"
		default:
			return unit
		}
	}
	return unit
}

func generateEgressSignature(protocol string, payload []byte) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return fallbackSignature(protocol, payload)
	}
	switch protocol {
	case "mongodb", "postgresql", "redis":
		return databaseSignature(protocol, obj)
	case "rabbitmq", "kafka":
		return queueSignature(protocol, obj)
	default:
		return fallbackSignature(protocol, payload)
	}
}

func databaseSignature(protocol string, obj map[string]any) string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "$") {
			continue
		}
		if col, ok := obj[k].(string); ok && col != "" {
			return fmt.Sprintf("%s:%s:%s", protocol, k, col)
		}
	}
	return fallbackSignature(protocol, mustJSON(obj))
}

func queueSignature(protocol string, obj map[string]any) string {
	exchange := stringField(obj, "exchange")
	if exchange == "" {
		exchange = stringField(obj, "exchange_name")
	}
	routingKey := stringField(obj, "routing_key")
	if routingKey == "" {
		routingKey = stringField(obj, "routingKey")
	}
	if exchange != "" && routingKey != "" {
		return fmt.Sprintf("%s:publish:%s:%s", protocol, exchange, routingKey)
	}
	if routingKey != "" {
		return fmt.Sprintf("%s:publish:%s", protocol, routingKey)
	}
	if exchange != "" {
		return fmt.Sprintf("%s:publish:%s", protocol, exchange)
	}
	return fallbackSignature(protocol, mustJSON(obj))
}

func stringField(obj map[string]any, key string) string {
	v, ok := obj[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func fallbackSignature(protocol string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%s:unknown:%x", protocol, sum[:4])
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

type sigSlot struct {
	aIndex int
	a      []byte
	b      []byte
}

func pairAndDiffEgress(
	protocol string,
	controlA, controlB, candidate [][]byte,
	userNoise map[string]struct{},
) ([]PathDiff, error) {
	// Build control-A index -> control-B payload map (same ordinal).
	bByAIndex := make(map[int][]byte, len(controlA))
	for i := range controlA {
		if i < len(controlB) {
			bByAIndex[i] = controlB[i]
		}
	}

	// Multiset queues per signature: FIFO of control-A slots.
	queues := make(map[string][]sigSlot)
	for i, a := range controlA {
		sig := generateEgressSignature(protocol, a)
		queues[sig] = append(queues[sig], sigSlot{aIndex: i, a: a, b: bByAIndex[i]})
	}

	var allRegs []PathDiff
	for _, c := range candidate {
		sig := generateEgressSignature(protocol, c)
		q := queues[sig]
		if len(q) == 0 {
			allRegs = append(allRegs, PathDiff{
				Path:     fmt.Sprintf("(extra egress: %s)", sig),
				Expected: "<none>",
				Actual:   string(c),
			})
			continue
		}
		slot := q[0]
		queues[sig] = q[1:]

		var noise map[string]struct{}
		if slot.b != nil {
			var err error
			noise, err = NoisePaths(slot.a, slot.b)
			if err != nil {
				return nil, err
			}
		}
		merged := MergeNoise(noise, userNoise)
		regs, err := Regressions(slot.a, c, merged)
		if err != nil {
			return nil, err
		}
		allRegs = append(allRegs, regs...)
	}
	return allRegs, nil
}

func copyByteSlices(in [][]byte) [][]byte {
	if in == nil {
		return nil
	}
	out := make([][]byte, len(in))
	for i, b := range in {
		out[i] = append([]byte(nil), b...)
	}
	return out
}
