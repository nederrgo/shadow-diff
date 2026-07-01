package dashboard

import (
	"context"

	"github.com/shadow-diff/beru/internal/roles"
	v2diff "github.com/shadow-diff/beru/internal/v2/diff"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func listTraceSummaries(ctx context.Context, repo v2storage.TraceRepository, shadowTestName, statusFilter string, limit int) ([]v2storage.TraceSummary, error) {
	groups, err := repo.ListTraceGroups(ctx, shadowTestName, limit*3)
	if err != nil {
		return nil, err
	}
	var out []v2storage.TraceSummary
	for _, g := range groups {
		reports, err := repo.ListReports(ctx, g.TraceID, g.Protocol)
		if err != nil {
			return nil, err
		}
		if g.Protocol == "http" {
			for _, dir := range []v2storage.PayloadDirection{v2storage.DirectionIngress, v2storage.DirectionEgress} {
				subset := filterByProtocolAndDirection(reports, g.Protocol, dir)
				if len(subset) == 0 || !protocolHasAllRoles(subset, g.Protocol) {
					continue
				}
				status := v2diff.EvaluateTraceHistory(subset).Status
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
		if !protocolHasAllRoles(reports, g.Protocol) {
			continue
		}
		subset := filterByProtocol(reports, g.Protocol)
		status := v2diff.EvaluateTraceHistory(subset).Status
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

func protocolHasAllRoles(reports []v2storage.RawReport, protocol string) bool {
	have := make(map[string]struct{})
	for _, r := range reports {
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

func filterByProtocol(reports []v2storage.RawReport, protocol string) []v2storage.RawReport {
	var out []v2storage.RawReport
	for _, r := range reports {
		if r.Protocol == protocol {
			out = append(out, r)
		}
	}
	return out
}
