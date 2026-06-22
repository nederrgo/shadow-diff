package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/shadow-diff/siphon/internal/capture"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/session"
)

type Server struct {
	addr         string
	cfgMgr       *config.Manager
	sessionMap   *session.SessionMap
	capMgr       *capture.CaptureManager
	forwardCount *uint64
	pcapAddr     string
}

func NewServer(addr string, cfgMgr *config.Manager, sessionMap *session.SessionMap, capMgr *capture.CaptureManager, forwardCount *uint64, pcapAddr string) *Server {
	return &Server{
		addr:         addr,
		cfgMgr:       cfgMgr,
		sessionMap:   sessionMap,
		capMgr:       capMgr,
		forwardCount: forwardCount,
		pcapAddr:     pcapAddr,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/config", s.handleConfig)
	mux.HandleFunc("/v1/status", s.handleStatus)

	log.Printf("HTTP control API listening on %s", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload config.SiphonConfig
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Printf("Error decoding config payload: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	s.cfgMgr.Update(payload)
	for _, t := range payload.Targets {
		log.Printf("Siphon target %q: prod_ips=%v recordAndReplay=%d recorder_host=%q",
			t.ShadowTest, t.TargetIPs, len(t.RecordAndReplay), t.RecorderHost)
	}
	log.Printf("Received configuration update: %d targets", len(payload.Targets))

	if s.cfgMgr.HasAnyTargets() {
		if err := s.capMgr.Start(s.pcapAddr); err != nil {
			log.Printf("Error starting capture: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"success"}`))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	pcapAddr, frames, packets := s.capMgr.Status()
	activeSessions := s.sessionMap.ActiveCount()
	cfg := s.cfgMgr.GetConfig()
	targetsCount := len(cfg.Targets)
	sampleRate := cfg.SampleRate

	recordAndReplayCount := 0
	recorderHostConfigured := false
	for _, t := range cfg.Targets {
		recordAndReplayCount += len(t.RecordAndReplay)
		if t.RecorderHost != "" {
			recorderHostConfigured = true
		}
	}

	status := map[string]interface{}{
		"pcap_listen_addr":         pcapAddr,
		"frames_read":              frames,
		"packets":                  packets,
		"requests_forwarded":       atomic.LoadUint64(s.forwardCount),
		"sample_rate":              sampleRate,
		"active_sessions":          activeSessions,
		"targets_count":            targetsCount,
		"record_and_replay_count":  recordAndReplayCount,
		"recorder_host_configured": recorderHostConfigured,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("Error encoding status response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
