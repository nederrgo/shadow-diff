package api

import (
	"encoding/json"
	"net/http"
	"strings"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
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
		reports = filterByProtocolAndDirection(allReports, protocol, v2storage.PayloadDirection(direction))
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

func filterByProtocolAndDirection(reports []v2storage.RawReport, protocol string, direction v2storage.PayloadDirection) []v2storage.RawReport {
	var out []v2storage.RawReport
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
	if direction == string(v2storage.DirectionIngress) || direction == string(v2storage.DirectionEgress) {
		return direction
	}
	return string(v2storage.DirectionIngress)
}
