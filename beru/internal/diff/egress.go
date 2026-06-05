package diff

import (
	"fmt"
	"log/slog"
)

// AnalyzeEgress runs diff-of-diffs on generic protocol egress payloads.
func AnalyzeEgress(log *slog.Logger, traceID, protocol string, bodyA, bodyB, bodyC []byte) {
	if log == nil {
		log = slog.Default()
	}
	if bodyA == nil {
		log.Error("Could not run egress diff without control-a payload", "traceID", traceID, "protocol", protocol)
		return
	}
	var noise map[string]struct{}
	var err error
	if bodyB != nil {
		noise, err = NoisePaths(bodyA, bodyB)
		if err != nil {
			log.Error("Could not compare control pods for egress noise", "traceID", traceID, "protocol", protocol, "err", err)
			return
		}
	}
	if bodyC == nil {
		log.Info(fmt.Sprintf("Timed out waiting for Trace %s (%s egress): missing candidate", traceID, protocol))
		return
	}
	regs, err := Regressions(bodyA, bodyC, noise)
	if err != nil {
		log.Error("Could not compare control-a and candidate egress", "traceID", traceID, "protocol", protocol, "err", err)
		return
	}
	if len(regs) == 0 {
		log.Info(fmt.Sprintf("No egress regression for Trace %s (%s)", traceID, protocol))
		return
	}
	ignored := formatIgnoredNoise(noise)
	for _, r := range regs {
		log.Info(fmt.Sprintf(
			"Egress regression for Trace %s (%s): Field '%s' expected %s but got %s%s",
			traceID, protocol, r.Path, r.Expected, r.Actual, ignored,
		))
	}
}

// AnalyzeMongoEgress runs diff-of-diffs on MongoDB egress query payloads.
func AnalyzeMongoEgress(log *slog.Logger, traceID string, bodyA, bodyB, bodyC []byte) {
	AnalyzeEgress(log, traceID, "mongodb", bodyA, bodyB, bodyC)
}
