package dashboard

import (
	"strconv"
	"strings"

	"github.com/shadow-diff/beru/internal/roles"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

const (
	placeholderNoQuery     = "[ NO QUERY EXECUTED ]"
	placeholderNoMessage   = "[ NO MESSAGE PUBLISHED ]"
	placeholderNoOperation = "[ NO OPERATION EXECUTED ]"
)

type sequenceStepView struct {
	Index       int
	Label       string
	Signature   string
	Expected    string
	Actual      string
	IsExtra     bool
	IsMissing   bool
	HasExpected bool
	HasActual   bool
}

func buildSequenceStepsFromReports(protocol string, reports []v2storage.RawReport) []sequenceStepView {
	controlA := reportsForRole(reports, roles.ControlA)
	candidate := reportsForRole(reports, roles.Candidate)
	lastStep := len(controlA)
	if len(candidate) > lastStep {
		lastStep = len(candidate)
	}
	if lastStep == 0 {
		return nil
	}

	placeholder := missingPlaceholder(protocol)
	unit := sequenceUnit(protocol)
	steps := make([]sequenceStepView, 0, lastStep)
	for i := 0; i < lastStep; i++ {
		step := sequenceStepView{Index: i, Label: unit + " " + strconv.Itoa(i+1)}
		if i < len(controlA) {
			step.HasExpected = true
			step.Expected = PrettyDisplayJSON(string(controlA[i].PayloadBytes))
			step.Signature = controlA[i].Signature
		} else {
			step.Expected = placeholder
		}
		if i < len(candidate) {
			step.HasActual = true
			step.Actual = PrettyDisplayJSON(string(candidate[i].PayloadBytes))
			if step.Signature == "" {
				step.Signature = candidate[i].Signature
			}
		} else {
			step.Actual = placeholder
		}
		step.IsExtra = !step.HasExpected && step.HasActual
		step.IsMissing = step.HasExpected && !step.HasActual
		steps = append(steps, step)
	}
	return steps
}

func reportsForRole(reports []v2storage.RawReport, role string) []v2storage.RawReport {
	var out []v2storage.RawReport
	for _, r := range reports {
		if r.ShadowRole == role {
			out = append(out, r)
		}
	}
	return out
}

func missingPlaceholder(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "mongodb", "postgresql", "redis":
		return placeholderNoQuery
	case "rabbitmq", "kafka":
		return placeholderNoMessage
	default:
		return placeholderNoOperation
	}
}

func sequenceUnit(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "mongodb", "postgresql", "redis":
		return "Query"
	case "rabbitmq", "kafka":
		return "Message"
	default:
		return "Operation"
	}
}

func signaturesFromReports(reports []v2storage.RawReport) string {
	var parts []string
	for _, r := range reports {
		if r.ShadowRole != roles.ControlA || r.Signature == "" {
			continue
		}
		parts = append(parts, r.Signature)
	}
	return strings.Join(parts, ", ")
}
