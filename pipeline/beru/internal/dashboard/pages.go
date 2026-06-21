package dashboard

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

type indexPage struct {
	Runs           []runView
	SelectedRunID  int64
	Filter         string
	TotalTraces    int
	MismatchCount  int
	MatchRate      float64
	Traces         []traceView
	ShadowTestName string
}

type runView struct {
	ID            int64
	Name          string
	StartTime     string
	TotalTraces   int
	MismatchCount int
	MatchRate     float64
}

type traceView struct {
	ID        int64
	TraceID   string
	Protocol  string
	Status    string
	Timestamp string
}

type tracePage struct {
	Trace          traceView
	Related        []traceView
	SequenceSteps  []sequenceStepView
	Mismatches     []mismatchView
	LeftLines      []DiffLine
	RightLines     []DiffLine
	ShadowTestName string
}

type mismatchView struct {
	Path          string
	ExpectedValue string
	ActualValue   string
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.DB == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}

	page := indexPage{Filter: "all"}
	runs, err := h.DB.ListShadowTests(r.Context(), 50)
	if err != nil {
		http.Error(w, "Could not load runs", http.StatusInternalServerError)
		return
	}
	for _, run := range runs {
		page.Runs = append(page.Runs, runView{
			ID: run.ID, Name: run.Name, StartTime: run.StartTime,
			TotalTraces: run.TotalTraces, MismatchCount: run.MismatchCount, MatchRate: run.MatchRate,
		})
	}

	selectedID, _ := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	if selectedID == 0 && len(runs) > 0 {
		selectedID = runs[0].ID
	}
	page.SelectedRunID = selectedID

	if selectedID > 0 {
		st, err := h.DB.GetShadowTest(r.Context(), selectedID)
		if err == nil {
			page.TotalTraces = st.TotalTraces
			page.MismatchCount = st.MismatchCount
			page.MatchRate = st.MatchRate
			page.ShadowTestName = st.Name
		}
		filter := r.URL.Query().Get("filter")
		if filter == "" {
			filter = "all"
		}
		page.Filter = filter
		statusFilter := filter
		if statusFilter == "match" {
			statusFilter = "MATCH"
		} else if statusFilter == "mismatch" {
			statusFilter = "MISMATCH"
		} else if statusFilter == "all" {
			statusFilter = ""
		}
		traces, err := h.DB.ListTraces(r.Context(), selectedID, statusFilter, 200)
		if err == nil {
			for _, t := range traces {
				page.Traces = append(page.Traces, traceView{
					ID: t.ID, TraceID: t.TraceID, Protocol: t.Protocol,
					Status: t.Status, Timestamp: t.Timestamp,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, "layout", page); err != nil {
		h.Log.Error("Template render failed", "err", err)
	}
}

func (h *Handler) handleTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.DB == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/dashboard/traces/")
	id, err := strconv.ParseInt(strings.Trim(idStr, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}

	trace, err := h.DB.GetTraceByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	page := tracePage{
		Trace: traceView{
			ID: trace.ID, TraceID: trace.TraceID, Protocol: trace.Protocol,
			Status: trace.Status, Timestamp: trace.Timestamp,
		},
		ShadowTestName: trace.ShadowTestName,
	}
	mismatches, err := h.DB.ListMismatchesForTrace(r.Context(), trace.TraceID, trace.Protocol)
	if err == nil {
		for _, m := range mismatches {
			page.Mismatches = append(page.Mismatches, mismatchView{
				Path: m.Path, ExpectedValue: m.ExpectedValue, ActualValue: m.ActualValue,
			})
		}
	}
	payloads, err := h.DB.ListEgressPayloads(r.Context(), trace.TraceID, trace.Protocol)
	if err == nil && len(payloads) > 0 {
		page.SequenceSteps = buildSequenceSteps(trace.Protocol, payloads)
	} else {
		bodyA, bodyC, err := h.DB.MismatchBodies(r.Context(), trace.TraceID, trace.Protocol)
		if err == nil || err == sql.ErrNoRows {
			page.LeftLines, page.RightLines = RenderLineDiff(bodyA, bodyC)
		}
	}
	related, err := h.DB.ListTracesByTraceID(r.Context(), trace.ShadowTestID, trace.TraceID)
	if err == nil {
		for _, t := range related {
			page.Related = append(page.Related, traceView{
				ID: t.ID, TraceID: t.TraceID, Protocol: t.Protocol,
				Status: t.Status, Timestamp: t.Timestamp,
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, "trace-layout", page); err != nil {
		h.Log.Error("Template render failed", "err", err)
	}
}
