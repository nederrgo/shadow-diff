package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

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
	interfaceEnv string
}

func NewServer(addr string, cfgMgr *config.Manager, sessionMap *session.SessionMap, capMgr *capture.CaptureManager, forwardCount *uint64, interfaceEnv string) *Server {
	return &Server{
		addr:         addr,
		cfgMgr:       cfgMgr,
		sessionMap:   sessionMap,
		capMgr:       capMgr,
		forwardCount: forwardCount,
		interfaceEnv: interfaceEnv,
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
	log.Printf("Received configuration update: %d targets", len(payload.Targets))

	// Dynamically start capturing when first valid config is received
	if s.cfgMgr.HasAnyTargets() {
		if err := s.capMgr.Start(s.interfaceEnv); err != nil {
			log.Printf("Error starting capture: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// Handles may not be registered yet; retry BPF attach after capture loops start.
		var applyErr error
		for attempt := 0; attempt < 25; attempt++ {
			applyErr = s.capMgr.ApplyBPFFilter()
			if applyErr == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if applyErr != nil {
			log.Printf("Error applying BPF filter to interfaces: %v", applyErr)
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

	ifaces, frames, packets := s.capMgr.Status()
	activeSessions := s.sessionMap.ActiveCount()
	targetsCount := len(s.cfgMgr.GetConfig().Targets)
	sampleRate := s.cfgMgr.GetConfig().SampleRate

	status := map[string]interface{}{
		"interfaces":         ifaces,
		"frames_read":        frames,
		"packets":            packets,
		"requests_forwarded": atomic.LoadUint64(s.forwardCount),
		"sample_rate":        sampleRate,
		"active_sessions":    activeSessions,
		"targets_count":      targetsCount,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("Error encoding status response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
