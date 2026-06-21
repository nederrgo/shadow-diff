package dashboard

import (
	"strings"
	"strconv"

	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/storage"
)

const (
	placeholderNoQuery      = "[ NO QUERY EXECUTED ]"
	placeholderNoMessage    = "[ NO MESSAGE PUBLISHED ]"
	placeholderNoOperation  = "[ NO OPERATION EXECUTED ]"
)

type sequenceStepView struct {
	Index       int
	Label       string
	Expected    string
	Actual      string
	IsExtra     bool
	IsMissing   bool
	HasExpected bool
	HasActual   bool
}

func buildSequenceSteps(protocol string, payloads []storage.EgressPayload) []sequenceStepView {
	if len(payloads) == 0 {
		return nil
	}
	byWorkload := map[string]map[int]string{
		roles.ControlA:  {},
		roles.Candidate: {},
	}
	aMax, cMax := -1, -1
	for _, p := range payloads {
		switch p.Workload {
		case roles.ControlA:
			byWorkload[roles.ControlA][p.SequenceIndex] = p.PayloadJSON
			if p.SequenceIndex > aMax {
				aMax = p.SequenceIndex
			}
		case roles.Candidate:
			byWorkload[roles.Candidate][p.SequenceIndex] = p.PayloadJSON
			if p.SequenceIndex > cMax {
				cMax = p.SequenceIndex
			}
		}
	}
	lastStep := aMax
	if cMax > lastStep {
		lastStep = cMax
	}
	if lastStep < 0 {
		return nil
	}

	placeholder := missingPlaceholder(protocol)
	unit := sequenceUnit(protocol)
	steps := make([]sequenceStepView, 0, lastStep+1)
	for i := 0; i <= lastStep; i++ {
		step := sequenceStepView{Index: i, Label: unit + " " + strconv.Itoa(i+1)}
		if raw, ok := byWorkload[roles.ControlA][i]; ok && raw != "" {
			step.HasExpected = true
			step.Expected = PrettyDisplayJSON(raw)
		} else {
			step.Expected = placeholder
		}
		if raw, ok := byWorkload[roles.Candidate][i]; ok && raw != "" {
			step.HasActual = true
			step.Actual = PrettyDisplayJSON(raw)
		} else {
			step.Actual = placeholder
		}
		step.IsExtra = !step.HasExpected && step.HasActual
		step.IsMissing = step.HasExpected && !step.HasActual
		steps = append(steps, step)
	}
	return steps
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
