package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/v2/diff"
	"github.com/shadow-diff/beru/internal/v2/storage"
)

func mirrorLegacyLogs(traceID string, history []storage.RawReport, verdict *storage.VerdictState) {
	if traceID == "" || verdict == nil {
		return
	}
	log := slog.Default()
	for _, protocol := range protocolsInHistory(history) {
		byProto := filterHistoryByProtocol(history, protocol)
		if isIngressProtocol(protocol) {
			// HTTP carries both ingress (Envoy ext_proc) and egress (Shop→Beru).
			// Evaluate and mirror each direction independently.
			ingress := filterHistoryByDirection(byProto, storage.DirectionIngress)
			if len(ingress) > 0 && protocolHasAllRoles(ingress, protocol) {
				if pv := diff.EvaluateTraceHistory(ingress, nil, diff.EvalOptions{}); pv != nil {
					mirrorIngressLogs(log, traceID, pv)
				}
			}
			egress := filterHistoryByDirection(byProto, storage.DirectionEgress)
			if len(egress) > 0 && protocolHasAllRoles(egress, protocol) {
				if pv := diff.EvaluateTraceHistory(egress, nil, diff.EvalOptions{}); pv != nil {
					mirrorEgressLogs(log, traceID, protocol, egress, pv)
				}
			}
			continue
		}
		if !protocolHasAllRoles(byProto, protocol) {
			continue
		}
		pv := diff.EvaluateTraceHistory(byProto, nil, diff.EvalOptions{})
		if pv == nil {
			continue
		}
		mirrorEgressLogs(log, traceID, protocol, byProto, pv)
	}
}

func mirrorIngressLogs(log *slog.Logger, traceID string, verdict *storage.VerdictState) {
	switch verdict.Status {
	case storage.StatusMatch:
		log.Info(fmt.Sprintf("No regression for Trace %s", traceID))
	case storage.StatusMismatch:
		for _, step := range parseSteps(verdict.SummaryDetails) {
			if step.Kind == storage.FlagMismatchPayload {
				log.Info(fmt.Sprintf(
					"Regression found in Trace %s: Field '%s' expected <control-a> but got <candidate>.",
					traceID, step.Detail,
				))
			}
		}
	case storage.StatusVoidedBaselineDivergence:
		log.Info(fmt.Sprintf("Voided baseline divergence for Trace %s", traceID))
	case storage.StatusWaitingForRoles:
		log.Info(fmt.Sprintf("Waiting for roles on Trace %s", traceID))
	}
}

func mirrorEgressLogs(log *slog.Logger, traceID, protocol string, history []storage.RawReport, verdict *storage.VerdictState) {
	switch verdict.Status {
	case storage.StatusMatch:
		log.Info(fmt.Sprintf("No egress regression for Trace %s (%s)", traceID, protocol))
	case storage.StatusMismatch:
		steps := parseSteps(verdict.SummaryDetails)
		// Compound: emit count AND payload (no short-circuit).
		if verdict.HasCountRegression {
			controlA := roleCount(history, roles.ControlA)
			candidate := roleCount(history, roles.Candidate)
			unit := egressCountUnit(protocol)
			log.Info(fmt.Sprintf(
				"Egress count regression for Trace %s (%s): expected %d %s but got %d",
				traceID, protocol, controlA, formatCountUnit(unit, controlA), candidate,
			))
		}
		for _, step := range steps {
			if step.Kind == storage.FlagMismatchPayload {
				log.Info(fmt.Sprintf(
					"Egress regression for Trace %s (%s): Field '%s' expected <control-a> but got <candidate>",
					traceID, protocol, step.Detail,
				))
			}
		}
	case storage.StatusVoidedBaselineDivergence:
		log.Info(fmt.Sprintf("Voided egress baseline divergence for Trace %s (%s)", traceID, protocol))
	case storage.StatusWaitingForRoles:
		log.Info(fmt.Sprintf("Waiting for egress roles on Trace %s (%s)", traceID, protocol))
	}
}

func parseSteps(summaryDetails string) []storage.VerdictStep {
	if summaryDetails == "" {
		return nil
	}
	var details storage.VerdictDetails
	if err := json.Unmarshal([]byte(summaryDetails), &details); err != nil {
		return nil
	}
	return details.Steps
}

func isIngressProtocol(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "http", "ingress":
		return true
	default:
		return false
	}
}

func protocolsInHistory(history []storage.RawReport) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, r := range history {
		if _, ok := seen[r.Protocol]; ok {
			continue
		}
		seen[r.Protocol] = struct{}{}
		out = append(out, r.Protocol)
	}
	return out
}

func filterHistoryByProtocol(history []storage.RawReport, protocol string) []storage.RawReport {
	var out []storage.RawReport
	for _, r := range history {
		if r.Protocol == protocol {
			out = append(out, r)
		}
	}
	return out
}

func filterHistoryByDirection(history []storage.RawReport, direction storage.PayloadDirection) []storage.RawReport {
	var out []storage.RawReport
	for _, r := range history {
		if r.Direction == direction || (direction == storage.DirectionIngress && r.Direction == "") {
			if r.Direction == storage.DirectionEgress && direction == storage.DirectionIngress {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

func protocolHasAllRoles(history []storage.RawReport, protocol string) bool {
	have := make(map[string]struct{})
	for _, r := range history {
		if r.Protocol != protocol {
			continue
		}
		have[r.ShadowRole] = struct{}{}
	}
	for _, role := range roles.All {
		if _, ok := have[role]; !ok {
			return false
		}
	}
	return true
}

func roleCount(history []storage.RawReport, role string) int {
	n := 0
	for _, r := range history {
		if r.ShadowRole == role {
			n++
		}
	}
	return n
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
