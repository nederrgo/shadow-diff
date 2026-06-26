package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) handleAPIShadowTests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.DB == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	runs, err := h.DB.ListShadowTests(r.Context(), 50)
	if err != nil {
		http.Error(w, "Could not list shadow tests", http.StatusInternalServerError)
		return
	}
	writeJSON(w, runs)
}

func (h *Handler) handleAPITraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.DB == nil || h.Repo == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	shadowTestName := h.DB.DefaultShadowTestName()
	runID, _ := strconv.ParseInt(r.URL.Query().Get("shadow_test_id"), 10, 64)
	if runID > 0 {
		if st, err := h.DB.GetShadowTest(r.Context(), runID); err == nil {
			shadowTestName = st.Name
		}
	} else {
		runs, err := h.DB.ListShadowTests(r.Context(), 1)
		if err == nil && len(runs) > 0 {
			shadowTestName = runs[0].Name
		}
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		if f := r.URL.Query().Get("filter"); f == "match" {
			status = "MATCH"
		} else if f == "mismatch" {
			status = "MISMATCH"
		}
	}
	traces, err := listTraceSummaries(r.Context(), h.Repo, shadowTestName, status, 500)
	if err != nil {
		http.Error(w, "Could not list traces", http.StatusInternalServerError)
		return
	}
	writeJSON(w, traces)
}

func (h *Handler) handleAPITraceByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Repo == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	traceID := traceIDFromAPIPath(r.URL.Path)
	protocol := r.URL.Query().Get("protocol")
	if traceID == "" || protocol == "" {
		http.Error(w, "trace id and protocol are required", http.StatusBadRequest)
		return
	}
	reports, err := h.Repo.ListReports(r.Context(), traceID, protocol)
	if err != nil || len(reports) == 0 {
		http.Error(w, "Trace not found", http.StatusNotFound)
		return
	}
	verdict, _ := h.Repo.GetVerdict(r.Context(), traceID)
	resp := map[string]any{
		"trace_id": traceID,
		"protocol": protocol,
		"reports":  reports,
		"verdict":  verdict,
	}
	if isEgressProtocol(protocol) {
		resp["sequence_steps"] = buildSequenceStepsFromReports(protocol, reports)
	} else {
		bodyA, bodyC := ingressBodies(reports)
		resp["body_a"] = json.RawMessage(bodyA)
		resp["body_c"] = json.RawMessage(bodyC)
	}
	resp["mismatches"] = mismatchesForProtocol(verdict, protocol)
	writeJSON(w, resp)
}

type noiseFilterRequest struct {
	ShadowTestName string `json:"shadow_test_name"`
	Path           string `json:"path"`
}

func (h *Handler) handleAPINoiseFilters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.DB == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	var req noiseFilterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if req.ShadowTestName == "" {
		req.ShadowTestName = h.DB.DefaultShadowTestName()
	}
	if err := h.DB.AddNoiseFilter(context.Background(), req.ShadowTestName, req.Path); err != nil {
		http.Error(w, "Could not save filter", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"status": "saved"})
}

func traceIDFromAPIPath(path string) string {
	return strings.Trim(strings.TrimPrefix(path, "/api/v1/traces/"), "/")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
