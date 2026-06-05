package diff

import (
	"fmt"
	"log/slog"
)

// AnalyzeMongoEgress runs diff-of-diffs on MongoDB egress query payloads.
func AnalyzeMongoEgress(log *slog.Logger, traceID string, bodyA, bodyB, bodyC []byte) {
	if log == nil {
		log = slog.Default()
	}
	noise, err := NoisePaths(bodyA, bodyB)
	if err != nil {
		log.Error("Could not compare control pods for mongo egress noise", "traceID", traceID, "err", err)
		return
	}
	regs, err := Regressions(bodyA, bodyC, noise)
	if err != nil {
		log.Error("Could not compare control-a and candidate mongo egress", "traceID", traceID, "err", err)
		return
	}
	if len(regs) == 0 {
		log.Info(fmt.Sprintf("No egress regression for Trace %s (mongodb)", traceID))
		return
	}
	ignored := formatIgnoredNoise(noise)
	for _, r := range regs {
		log.Info(fmt.Sprintf(
			"Egress regression for Trace %s (mongodb): Field '%s' expected %s but got %s%s",
			traceID, r.Path, r.Expected, r.Actual, ignored,
		))
	}
}
