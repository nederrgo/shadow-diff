package dashboard

import (
	"context"
	"encoding/json"

	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/storage"
	v2diff "github.com/shadow-diff/beru/internal/v2/diff"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func listTraceSummaries(ctx context.Context, repo v2storage.TraceRepository, db storage.RunStore, shadowTestName, statusFilter string, limit int) ([]v2storage.TraceSummary, error) {
	var userNoise map[string]struct{}
	if db != nil && shadowTestName != "" {
		userNoise, _ = db.NoisePathsForTest(ctx, shadowTestName)
	}
	groups, err := repo.ListTraceGroups(ctx, shadowTestName, limit*3)
	if err != nil {
		return nil, err
	}
	var out []v2storage.TraceSummary
	seenTrace := make(map[string]string) // trace_id -> stored status (cached)
	for _, g := range groups {
		reports, err := repo.ListReports(ctx, g.TraceID, g.Protocol)
		if err != nil {
			return nil, err
		}

		storedStatus, ok := seenTrace[g.TraceID]
		if !ok {
			if v, err := repo.GetVerdict(ctx, g.TraceID); err == nil && v != nil {
				storedStatus = v.Status
			}
			seenTrace[g.TraceID] = storedStatus
		}

		if g.Protocol == "http" {
			for _, dir := range []v2storage.PayloadDirection{v2storage.DirectionIngress, v2storage.DirectionEgress} {
				subset := filterByProtocolAndDirection(reports, g.Protocol, dir)
				if len(subset) == 0 {
					continue
				}
				status := resolveStatus(storedStatus, subset, userNoise)
				if status == "" {
					continue
				}
				if statusFilter != "" && status != statusFilter {
					continue
				}
				out = append(out, v2storage.TraceSummary{
					TraceID:        g.TraceID,
					Protocol:       g.Protocol,
					Direction:      dir,
					ShadowTestName: shadowTestName,
					LastCapturedAt: g.LastCapturedAt,
					Status:         status,
					Signatures:     signaturesFromReports(subset),
				})
				if len(out) >= limit {
					return out, nil
				}
			}
			continue
		}

		subset := filterByProtocol(reports, g.Protocol)
		if len(subset) == 0 {
			continue
		}
		status := resolveStatus(storedStatus, subset, userNoise)
		if status == "" {
			continue
		}
		if statusFilter != "" && status != statusFilter {
			continue
		}
		out = append(out, v2storage.TraceSummary{
			TraceID:        g.TraceID,
			Protocol:       g.Protocol,
			ShadowTestName: shadowTestName,
			LastCapturedAt: g.LastCapturedAt,
			Status:         status,
			Signatures:     signaturesFromReports(subset),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// resolveStatus prefers the stored verdict for WAITING_FOR_ROLES / VOIDED_*;
// otherwise live-evaluates when all roles are present for the subset.
func resolveStatus(stored string, subset []v2storage.RawReport, userNoise map[string]struct{}) string {
	switch stored {
	case v2storage.StatusWaitingForRoles, v2storage.StatusVoidedBaselineDivergence:
		return stored
	}
	if !protocolHasAllRoles(subset, subsetProtocol(subset)) {
		return ""
	}
	verdict := v2diff.EvaluateTraceHistory(subset, userNoise, v2diff.EvalOptions{})
	if verdict == nil {
		return ""
	}
	return verdict.Status
}

func subsetProtocol(reports []v2storage.RawReport) string {
	if len(reports) == 0 {
		return ""
	}
	return reports[0].Protocol
}

func protocolHasAllRoles(reports []v2storage.RawReport, protocol string) bool {
	have := make(map[string]struct{})
	for _, r := range reports {
		if protocol != "" && r.Protocol != protocol {
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

func filterByProtocol(reports []v2storage.RawReport, protocol string) []v2storage.RawReport {
	var out []v2storage.RawReport
	for _, r := range reports {
		if r.Protocol == protocol {
			out = append(out, r)
		}
	}
	return out
}

func parseVerdictDetails(summaryDetails string) v2storage.VerdictDetails {
	var details v2storage.VerdictDetails
	if summaryDetails == "" {
		return details
	}
	_ = json.Unmarshal([]byte(summaryDetails), &details)
	return details
}
