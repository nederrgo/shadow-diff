package diff

import (
	"fmt"
	"log/slog"
)

// AnalyzeEgress runs diff-of-diffs on generic protocol egress payloads.
func AnalyzeEgress(log *slog.Logger, traceID, protocol string, bodyA, bodyB, bodyC []byte, userNoise map[string]struct{}) Result {
	if log == nil {
		log = slog.Default()
	}
	res := Result{
		TraceID:  traceID,
		Protocol: protocol,
		BodyA:    append([]byte(nil), bodyA...),
	}
	if bodyC != nil {
		res.BodyC = append([]byte(nil), bodyC...)
	}
	if bodyA == nil {
		log.Error("Could not run egress diff without control-a payload", "traceID", traceID, "protocol", protocol)
		res.Err = fmt.Errorf("missing control-a payload")
		return res
	}
	var noise map[string]struct{}
	var err error
	if bodyB != nil {
		noise, err = NoisePaths(bodyA, bodyB)
		if err != nil {
			log.Error("Could not compare control pods for egress noise", "traceID", traceID, "protocol", protocol, "err", err)
			res.Err = err
			return res
		}
	}
	if bodyC == nil {
		log.Info(fmt.Sprintf("Timed out waiting for Trace %s (%s egress): missing candidate", traceID, protocol))
		res.Err = fmt.Errorf("missing candidate payload")
		return res
	}
	merged := MergeNoise(noise, userNoise)
	regs, err := Regressions(bodyA, bodyC, merged)
	if err != nil {
		log.Error("Could not compare control-a and candidate egress", "traceID", traceID, "protocol", protocol, "err", err)
		res.Err = err
		return res
	}
	res.Regressions = regs
	if len(regs) == 0 {
		res.Status = StatusMatch
		log.Info(fmt.Sprintf("No egress regression for Trace %s (%s)", traceID, protocol))
		return res
	}
	res.Status = StatusMismatch
	ignored := formatIgnoredNoise(merged)
	for _, r := range regs {
		log.Info(fmt.Sprintf(
			"Egress regression for Trace %s (%s): Field '%s' expected %s but got %s%s",
			traceID, protocol, r.Path, r.Expected, r.Actual, ignored,
		))
	}
	return res
}

// AnalyzeMongoEgress runs diff-of-diffs on MongoDB egress query payloads.
func AnalyzeMongoEgress(log *slog.Logger, traceID string, bodyA, bodyB, bodyC []byte, userNoise map[string]struct{}) Result {
	return AnalyzeEgress(log, traceID, "mongodb", bodyA, bodyB, bodyC, userNoise)
}
