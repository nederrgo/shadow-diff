package dashboard

import (
	"net/http"
	"strconv"
	"strings"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
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
	TraceID       string
	Protocol      string
	ProtocolLabel string
	Direction     string
	Status        string
	Timestamp     string
	Signature     string
	DetailURL     string
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
	if h.DB == nil || h.Repo == nil {
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
		})
	}

	selectedID, _ := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	if selectedID == 0 && len(runs) > 0 {
		selectedID = runs[0].ID
	}
	page.SelectedRunID = selectedID

	shadowTestName := h.DB.DefaultShadowTestName()
	if selectedID > 0 {
		if st, err := h.DB.GetShadowTest(r.Context(), selectedID); err == nil {
			shadowTestName = st.Name
			page.ShadowTestName = st.Name
		}
	}

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	page.Filter = filter
	statusFilter := ""
	if filter == "match" {
		statusFilter = "MATCH"
	} else if filter == "mismatch" {
		statusFilter = "MISMATCH"
	}

	summaries, err := listTraceSummaries(r.Context(), h.Repo, shadowTestName, statusFilter, 200)
	if err != nil {
		http.Error(w, "Could not list traces", http.StatusInternalServerError)
		return
	}
	for _, s := range summaries {
		page.Traces = append(page.Traces, summaryToView(s))
	}
	page.TotalTraces = len(summaries)
	for _, s := range summaries {
		if s.Status == "MISMATCH" {
			page.MismatchCount++
		}
	}
	if page.TotalTraces > 0 {
		page.MatchRate = float64(page.TotalTraces-page.MismatchCount) / float64(page.TotalTraces) * 100
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
	if h.DB == nil || h.Repo == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}

	traceID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dashboard/traces/"), "/")
	protocol := r.URL.Query().Get("protocol")
	direction := normalizeHTTPDirection(protocol, r.URL.Query().Get("direction"))
	if traceID == "" || protocol == "" {
		http.NotFound(w, r)
		return
	}

	allReports, err := h.Repo.ListReports(r.Context(), traceID, protocol)
	if err != nil || len(allReports) == 0 {
		http.NotFound(w, r)
		return
	}
	reports := allReports
	if protocol == "http" {
		reports = filterByProtocolAndDirection(allReports, protocol, v2storage.PayloadDirection(direction))
		if len(reports) == 0 {
			http.NotFound(w, r)
			return
		}
	}

	shadowTestName := reports[0].ShadowTestName
	if shadowTestName == "" {
		shadowTestName = h.DB.DefaultShadowTestName()
	}

	summaries, _ := listTraceSummaries(r.Context(), h.Repo, shadowTestName, "", 500)
	var current traceView
	for _, s := range summaries {
		if sameTraceView(traceID, protocol, direction, s) {
			current = summaryToView(s)
			break
		}
	}
	if current.TraceID == "" {
		dir := v2storage.PayloadDirection(direction)
		current = traceView{
			TraceID:       traceID,
			Protocol:      protocol,
			ProtocolLabel: protocolViewLabel(protocol, dir),
			Direction:     direction,
			Signature:     signaturesFromReports(reports),
			DetailURL:       traceDetailURL(traceID, protocol, dir),
		}
	}

	page := tracePage{
		Trace:          current,
		ShadowTestName: shadowTestName,
	}

	verdict, _ := h.Repo.GetVerdict(r.Context(), traceID)
	page.Mismatches = mismatchesForProtocol(verdict, protocol)

	if isEgressView(protocol, direction) {
		page.SequenceSteps = buildSequenceStepsFromReports(protocol, reports)
	} else {
		bodyA, bodyC := ingressBodies(reports)
		page.LeftLines, page.RightLines = RenderLineDiff(bodyA, bodyC)
	}

	for _, s := range summaries {
		if s.TraceID == traceID && !sameTraceView(traceID, protocol, direction, s) {
			page.Related = append(page.Related, summaryToView(s))
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, "trace-layout", page); err != nil {
		h.Log.Error("Template render failed", "err", err)
	}
}

func summaryToView(s v2storage.TraceSummary) traceView {
	return traceView{
		TraceID:       s.TraceID,
		Protocol:      s.Protocol,
		ProtocolLabel: protocolViewLabel(s.Protocol, s.Direction),
		Direction:     string(s.Direction),
		Status:        s.Status,
		Timestamp:     s.LastCapturedAt,
		Signature:     s.Signatures,
		DetailURL:     traceDetailURL(s.TraceID, s.Protocol, s.Direction),
	}
}

func isEgressProtocol(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "http", "ingress":
		return false
	default:
		return true
	}
}

func ingressBodies(reports []v2storage.RawReport) ([]byte, []byte) {
	var bodyA, bodyC []byte
	for _, rep := range reports {
		if rep.Protocol != "http" || rep.Direction != v2storage.DirectionIngress {
			continue
		}
		switch rep.ShadowRole {
		case "control-a":
			if len(bodyA) == 0 {
				bodyA = rep.PayloadBytes
			}
		case "candidate":
			if len(bodyC) == 0 {
				bodyC = rep.PayloadBytes
			}
		}
	}
	return bodyA, bodyC
}

func mismatchesForProtocol(verdict *v2storage.VerdictState, protocol string) []mismatchView {
	if verdict == nil || verdict.SummaryDetails == "" {
		return nil
	}
	prefix := protocol + ":"
	var out []mismatchView
	for _, detail := range strings.Split(verdict.SummaryDetails, "; ") {
		detail = strings.TrimSpace(detail)
		if detail == "" {
			continue
		}
		if !strings.Contains(detail, prefix) && !strings.HasPrefix(detail, "count ") {
			if protocol != "http" {
				continue
			}
		}
		out = append(out, mismatchView{
			Path:          detail,
			ExpectedValue: "match",
			ActualValue:   detail,
		})
	}
	return out
}
