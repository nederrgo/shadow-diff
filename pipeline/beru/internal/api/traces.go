package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/shadow-diff/beru/internal/model"
)

// handleGetTrace serves GET /api/v1/traces/{id}?protocol=&direction= for bats
// and other non-UI consumers. Slim shape: reports + verdict only.
func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Repo == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	traceID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/traces/"), "/")
	protocol := r.URL.Query().Get("protocol")
	direction := normalizeHTTPDirection(protocol, r.URL.Query().Get("direction"))
	if traceID == "" || protocol == "" {
		http.Error(w, "trace id and protocol are required", http.StatusBadRequest)
		return
	}

	allReports, err := s.Repo.ListReports(r.Context(), traceID, protocol)
	if err != nil || len(allReports) == 0 {
		http.Error(w, "Trace not found", http.StatusNotFound)
		return
	}
	reports := allReports
	if protocol == "http" {
		reports = filterByProtocolAndDirection(allReports, protocol, model.PayloadDirection(direction))
		if len(reports) == 0 {
			http.Error(w, "Trace not found", http.StatusNotFound)
			return
		}
	}
	verdict, _ := s.Repo.GetVerdict(r.Context(), traceID)
	resp := map[string]any{
		"trace_id": traceID,
		"protocol": protocol,
		"reports":  reports,
		"verdict":  verdict,
	}
	if direction != "" {
		resp["direction"] = direction
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func filterByProtocolAndDirection(reports []model.RawReport, protocol string, direction model.PayloadDirection) []model.RawReport {
	var out []model.RawReport
	for _, r := range reports {
		if r.Protocol == protocol && r.Direction == direction {
			out = append(out, r)
		}
	}
	return out
}

func normalizeHTTPDirection(protocol, direction string) string {
	if protocol != "http" {
		return ""
	}
	direction = strings.TrimSpace(direction)
	if direction == string(model.DirectionIngress) || direction == string(model.DirectionEgress) {
		return direction
	}
	return string(model.DirectionIngress)
}
