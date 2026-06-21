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
	if h.DB == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	runID, _ := strconv.ParseInt(r.URL.Query().Get("shadow_test_id"), 10, 64)
	if runID == 0 {
		runs, err := h.DB.ListShadowTests(r.Context(), 1)
		if err != nil || len(runs) == 0 {
			writeJSON(w, []any{})
			return
		}
		runID = runs[0].ID
	}
	status := r.URL.Query().Get("status")
	traces, err := h.DB.ListTraces(r.Context(), runID, status, 500)
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
	if h.DB == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/traces/")
	id, err := strconv.ParseInt(strings.Trim(idStr, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid trace id", http.StatusBadRequest)
		return
	}
	trace, err := h.DB.GetTraceByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Trace not found", http.StatusNotFound)
		return
	}
	mismatches, err := h.DB.ListMismatchesForTrace(r.Context(), trace.TraceID, trace.Protocol)
	if err != nil {
		http.Error(w, "Could not load mismatches", http.StatusInternalServerError)
		return
	}
	payloads, _ := h.DB.ListEgressPayloads(r.Context(), trace.TraceID, trace.Protocol)
	related, _ := h.DB.ListTracesByTraceID(r.Context(), trace.ShadowTestID, trace.TraceID)
	resp := map[string]any{
		"trace":      trace,
		"related":    related,
		"mismatches": mismatches,
	}
	if len(payloads) > 0 {
		resp["sequence_steps"] = buildSequenceSteps(trace.Protocol, payloads)
		resp["egress_payloads"] = payloads
	} else {
		bodyA, bodyC, _ := h.DB.MismatchBodies(r.Context(), trace.TraceID, trace.Protocol)
		resp["body_a"] = json.RawMessage(bodyA)
		resp["body_c"] = json.RawMessage(bodyC)
	}
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
