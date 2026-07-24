package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shadow-diff/beru/internal/dashboard"
	"github.com/shadow-diff/beru/internal/otlp"
	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/storage"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2report "github.com/shadow-diff/beru/internal/v2/report"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

// Server exposes HTTP endpoints for egress diff ingest and the dashboard.
type Server struct {
	Log       *slog.Logger
	Router    *v2engine.TraceRouter
	OTLP      *otlp.Server
	DB        *storage.DB
	Dashboard *dashboard.Handler
}

type egressDiffRequest struct {
	TraceID        string          `json:"trace_id"`
	Workload       string          `json:"workload"`
	Protocol       string          `json:"protocol"`
	Payload        json.RawMessage `json:"payload"`
	ShadowTestName string          `json:"shadow_test_name"`
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/api/v1/egress/diff", s.handleEgressDiff)
	mux.HandleFunc("/api/v1/ingest/wire", s.handleWireIngest)
	mux.HandleFunc("/api/v1/debug/seed-reports", s.handleSeedReports)
	if s.OTLP != nil {
		mux.HandleFunc("/v1/traces", s.OTLP.HandleHTTP)
	}
	if s.Dashboard != nil {
		s.Dashboard.Register(mux)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/dashboard/", http.StatusTemporaryRedirect)
	})
	s.Log.Info("Beru HTTP API listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleEgressDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Router == nil {
		http.Error(w, "Egress diff not configured", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var req egressDiffRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if req.TraceID == "" || req.Workload == "" || req.Protocol == "" {
		http.Error(w, "trace_id, workload, and protocol are required", http.StatusBadRequest)
		return
	}
	if !roles.IsValid(req.Workload) {
		http.Error(w, "workload is invalid", http.StatusBadRequest)
		return
	}
	if len(req.Payload) == 0 || !json.Valid(req.Payload) {
		http.Error(w, "payload must be valid JSON", http.StatusBadRequest)
		return
	}

	shadowTest := req.ShadowTestName
	if shadowTest == "" && s.DB != nil {
		shadowTest = s.DB.DefaultShadowTestName()
	}
	rawReport, err := v2report.FromEgress(req.TraceID, req.Workload, req.Protocol, shadowTest, req.Payload)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	s.Router.Route(rawReport)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]struct{}{})
}

func (s *Server) handleWireIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Router == nil {
		http.Error(w, "Wire ingest not configured", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var env v2report.NetworkEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if env.ShadowTestName == "" && s.DB != nil {
		env.ShadowTestName = s.DB.DefaultShadowTestName()
	}
	rawReport, err := v2report.FromWireEnvelope(&env)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if rawReport.ShadowTestName == "" && s.DB != nil {
		rawReport.ShadowTestName = s.DB.DefaultShadowTestName()
	}
	s.Router.Route(rawReport)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]struct{}{})
}

// seedReportRequest mirrors RawReport fields for UI/bats history injection.
type seedReportRequest struct {
	TraceID        string          `json:"trace_id"`
	ShadowRole     string          `json:"shadow_role"`
	ShadowTestName string          `json:"shadow_test_name"`
	Protocol       string          `json:"protocol"`
	Direction      string          `json:"direction"`
	Signature      string          `json:"signature"`
	StatusCode     string          `json:"status_code"`
	Payload        json.RawMessage `json:"payload"`
	CapturedAt     time.Time       `json:"captured_at"`
}

type seedReportsBody struct {
	Reports []seedReportRequest `json:"reports"`
}

// handleSeedReports injects RawReports into the TraceRouter for dashboard/UI debugging.
// Used by bats verdict scenarios to mirror unit-test histories without live traffic.
func (s *Server) handleSeedReports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Router == nil {
		http.Error(w, "Seed not configured", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	var body seedReportsBody
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Reports) == 0 {
		http.Error(w, "reports array required", http.StatusBadRequest)
		return
	}

	defaultTest := ""
	if s.DB != nil {
		defaultTest = s.DB.DefaultShadowTestName()
	}

	accepted := 0
	for _, req := range body.Reports {
		if req.TraceID == "" || req.ShadowRole == "" || req.Protocol == "" {
			http.Error(w, "each report needs trace_id, shadow_role, protocol", http.StatusBadRequest)
			return
		}
		if !roles.IsValid(req.ShadowRole) {
			http.Error(w, "shadow_role is invalid", http.StatusBadRequest)
			return
		}
		dir := v2storage.DirectionEgress
		if req.Direction == string(v2storage.DirectionIngress) {
			dir = v2storage.DirectionIngress
		}
		name := req.ShadowTestName
		if name == "" {
			name = defaultTest
		}
		captured := req.CapturedAt
		if captured.IsZero() {
			captured = time.Now().UTC()
		}
		sig := req.Signature
		payload := []byte(req.Payload)
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		if sig == "" && dir == v2storage.DirectionEgress {
			if derived, err := v2report.FromEgress(req.TraceID, req.ShadowRole, req.Protocol, name, payload); err == nil {
				sig = derived.Signature
			}
		}
		s.Router.Route(&v2storage.RawReport{
			TraceID:        req.TraceID,
			ShadowRole:     req.ShadowRole,
			ShadowTestName: name,
			Protocol:       req.Protocol,
			Direction:      dir,
			Signature:      sig,
			StatusCode:     req.StatusCode,
			PayloadBytes:   append([]byte(nil), payload...),
			CapturedAt:     captured.UTC(),
		})
		accepted++
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": accepted})
}
