package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/diff"
	"github.com/shadow-diff/beru/internal/payload"
	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/storage"
)

// Config controls the pending-trace store.
type Config struct {
	TraceTTL          time.Duration
	MaxPendingTraces  int
	SweepInterval     time.Duration
}

func ConfigFromEnv() Config {
	cfg := Config{
		TraceTTL:         30 * time.Second,
		MaxPendingTraces: 5000,
		SweepInterval:    10 * time.Second,
	}
	if v := os.Getenv("BERU_TRACE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.TraceTTL = d
		}
	}
	if v := os.Getenv("BERU_MAX_PENDING_TRACES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxPendingTraces = n
		}
	}
	return cfg
}

type pendingTrace struct {
	deadline time.Time
	reports  map[string]*beruv1.TrafficReport
}

// Store correlates INGRESS reports by trace ID.
type Store struct {
	cfg               Config
	log               *slog.Logger
	codec             *payload.Registry
	pending           map[string]*pendingTrace
	order             []string
	mu                sync.Mutex
	OnIngressComplete func(traceID string)
	Storage           *storage.DB
}

func NewStore(log *slog.Logger, cfg Config) *Store {
	if log == nil {
		log = slog.Default()
	}
	s := &Store{
		cfg:     cfg,
		log:     log,
		codec:   payload.NewRegistry(),
		pending: make(map[string]*pendingTrace),
	}
	go s.sweepLoop(context.Background())
	return s
}

// Handle ingests a traffic report (async-safe).
func (s *Store) Handle(report *beruv1.TrafficReport) {
	if report == nil || report.TraceId == "" {
		return
	}
	if report.Direction != beruv1.Direction_INGRESS {
		return
	}
	if report.Role == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictExpiredLocked()
	s.enforceCapLocked()

	pt, ok := s.pending[report.TraceId]
	if !ok {
		pt = &pendingTrace{
			deadline: time.Now().Add(s.cfg.TraceTTL),
			reports:  make(map[string]*beruv1.TrafficReport),
		}
		s.pending[report.TraceId] = pt
		s.order = append(s.order, report.TraceId)
	}
	pt.reports[report.Role] = report

	if len(pt.reports) < len(roles.All) {
		return
	}

	for _, r := range roles.All {
		if _, ok := pt.reports[r]; !ok {
			return
		}
	}

	snap := s.snapshotAndRemoveLocked(report.TraceId)
	go s.runDiff(snap)
}

func (s *Store) snapshotAndRemoveLocked(traceID string) map[string]*beruv1.TrafficReport {
	pt := s.pending[traceID]
	delete(s.pending, traceID)
	s.removeFromOrderLocked(traceID)
	out := make(map[string]*beruv1.TrafficReport, len(pt.reports))
	for k, v := range pt.reports {
		out[k] = v
	}
	return out
}

func (s *Store) runDiff(reports map[string]*beruv1.TrafficReport) {
	a := reports[roles.ControlA]
	if a == nil {
		return
	}
	traceID := a.TraceId

	bodyA, err := s.normalizeBody(a)
	if err != nil {
		s.log.Info("Could not diff trace: payload not JSON", "traceID", traceID, "role", roles.ControlA, "err", err)
		return
	}
	bodyB, err := s.normalizeBody(reports[roles.ControlB])
	if err != nil {
		s.log.Info("Could not diff trace: payload not JSON", "traceID", traceID, "role", roles.ControlB, "err", err)
		return
	}
	bodyC, err := s.normalizeBody(reports[roles.Candidate])
	if err != nil {
		s.log.Info("Could not diff trace: payload not JSON", "traceID", traceID, "role", roles.Candidate, "err", err)
		return
	}

	shadowName := storage.ShadowTestNameFromMetadata(a.Payload.GetMetadata(), "")
	var userNoise map[string]struct{}
	if s.Storage != nil {
		shadowName = storage.ShadowTestNameFromMetadata(a.Payload.GetMetadata(), s.Storage.DefaultShadowTestName())
		var nerr error
		userNoise, nerr = s.Storage.NoisePathsForTest(context.Background(), shadowName)
		if nerr != nil {
			s.log.Error("Could not load noise filters", "traceID", traceID, "err", nerr)
		}
	}

	res := diff.Analyze(s.log, traceID, diff.ProtocolIngress, bodyA, bodyB, bodyC, userNoise)
	if s.Storage != nil && res.Err == nil {
		if err := s.Storage.SaveDiffResult(context.Background(), shadowName, res); err != nil {
			s.log.Error("Could not persist diff result", "traceID", traceID, "err", err)
		}
	}
	if cb := s.OnIngressComplete; cb != nil {
		cb(traceID)
	}
}

func (s *Store) normalizeBody(r *beruv1.TrafficReport) ([]byte, error) {
	if r == nil || r.Payload == nil {
		return nil, fmt.Errorf("empty payload")
	}
	meta := r.Payload.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	out, codecName, err := s.codec.Normalize(r.Payload.Body, meta, r.Payload.ContentType)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", codecName, err)
	}
	if codecName == "raw" {
		return nil, fmt.Errorf("non-JSON payload")
	}
	return out, nil
}

func (s *Store) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			s.evictExpiredLocked()
			s.mu.Unlock()
		}
	}
}

func (s *Store) evictExpiredLocked() {
	now := time.Now()
	for _, id := range s.copyOrderLocked() {
		pt, ok := s.pending[id]
		if !ok {
			continue
		}
		if now.Before(pt.deadline) {
			continue
		}
		if len(pt.reports) >= len(roles.All) {
			delete(s.pending, id)
			s.removeFromOrderLocked(id)
			continue
		}
		received, missing := roleSets(pt.reports)
		s.log.Info(fmt.Sprintf(
			"Timed out waiting for Trace %s (INGRESS): received %s; missing %s",
			id, formatRoleList(received), formatRoleList(missing),
		))
		delete(s.pending, id)
		s.removeFromOrderLocked(id)
	}
}

func (s *Store) enforceCapLocked() {
	for len(s.pending) >= s.cfg.MaxPendingTraces {
		s.evictExpiredLocked()
		if len(s.pending) < s.cfg.MaxPendingTraces {
			return
		}
		if len(s.order) == 0 {
			return
		}
		oldest := s.order[0]
		s.log.Warn(fmt.Sprintf("Ingest Map Full - Evicting Trace %s", oldest))
		delete(s.pending, oldest)
		s.removeFromOrderLocked(oldest)
	}
}

func (s *Store) copyOrderLocked() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

func (s *Store) removeFromOrderLocked(id string) {
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

func roleSets(reports map[string]*beruv1.TrafficReport) (received, missing []string) {
	have := make(map[string]struct{}, len(reports))
	for r := range reports {
		have[r] = struct{}{}
	}
	for _, r := range roles.All {
		if _, ok := have[r]; ok {
			received = append(received, r)
		} else {
			missing = append(missing, r)
		}
	}
	sort.Strings(received)
	sort.Strings(missing)
	return received, missing
}

func formatRoleList(roles []string) string {
	if len(roles) == 0 {
		return "[]"
	}
	return "[" + strings.Join(roles, ", ") + "]"
}
