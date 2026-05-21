package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/shadow-diff/siphon/internal/capture"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
)

// Server exposes health, status, and config endpoints.
type Server struct {
	log      *slog.Logger
	addr     string
	store    *config.Store
	engine   *capture.Engine
	forward  *forward.Forwarder
	hub      interface{ RequestCount() uint64 }
	mu       sync.Mutex
	reloadFn func(context.Context, config.Payload) error
}

func New(
	log *slog.Logger,
	addr string,
	store *config.Store,
	engine *capture.Engine,
	fwd *forward.Forwarder,
	hub interface{ RequestCount() uint64 },
	reloadFn func(context.Context, config.Payload) error,
) *Server {
	return &Server{
		log:      log,
		addr:     addr,
		store:    store,
		engine:   engine,
		forward:  fwd,
		hub:      hub,
		reloadFn: reloadFn,
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/config", s.handleConfig)

	srv := &http.Server{Addr: s.addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	s.log.Info("Siphon API listening", "addr", s.addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type statusResponse struct {
	BPFFilter        string   `json:"bpf_filter"`
	Interfaces       []string `json:"interfaces"`
	TargetCount      int      `json:"target_count"`
	FramesRead       uint64   `json:"frames_read"`
	FramesTCP        uint64   `json:"frames_tcp"`
	FramesUnmatched  uint64   `json:"frames_unmatched"`
	Packets          uint64   `json:"packets"`
	RequestsParsed   uint64   `json:"requests_parsed"`
	RequestsForwarded uint64  `json:"requests_forwarded"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.engine.Snapshot()
	payload := s.store.Get()
	var parsed uint64
	if s.hub != nil {
		parsed = s.hub.RequestCount()
	}
	resp := statusResponse{
		BPFFilter:         snap.BPFFilter,
		Interfaces:        snap.Interfaces,
		TargetCount:       len(payload.Targets),
		FramesRead:        snap.FramesRead,
		FramesTCP:         snap.FramesTCP,
		FramesUnmatched:   snap.FramesUnmatched,
		Packets:           snap.Packets,
		RequestsParsed:    parsed,
		RequestsForwarded: s.forward.ForwardedCount(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var payload config.Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := payload.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadFn(r.Context(), payload); err != nil {
		if errors.Is(err, capture.ErrReloadInProgress) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}
