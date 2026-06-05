package egressdiff

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shadow-diff/beru/internal/diff"
	"github.com/shadow-diff/beru/internal/roles"
)

// Config controls the egress diff pending store.
type Config struct {
	TraceTTL         time.Duration
	MaxPendingTraces int
	SweepInterval    time.Duration
	EgressWait       time.Duration
}

// ConfigFromEnv loads store configuration from environment variables.
func ConfigFromEnv() Config {
	cfg := Config{
		TraceTTL:         30 * time.Second,
		MaxPendingTraces: 5000,
		SweepInterval:    10 * time.Second,
		EgressWait:       5 * time.Second,
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
	if v := os.Getenv("BERU_EGRESS_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.EgressWait = d
		}
	}
	return cfg
}

// Report is one workload egress payload for diff correlation.
type Report struct {
	TraceID  string
	Workload string
	Protocol string
	Payload  []byte
}

type pendingEgress struct {
	deadline time.Time
	reports  map[string][]byte
	protocol string
	diffDone bool
	timer    *time.Timer
}

// Store correlates egress payloads by trace ID.
type Store struct {
	cfg     Config
	log     *slog.Logger
	pending map[string]*pendingEgress
	order   []string
	mu      sync.Mutex
}

// NewStore creates an egress diff store with background eviction.
func NewStore(log *slog.Logger, cfg Config) *Store {
	if log == nil {
		log = slog.Default()
	}
	s := &Store{
		cfg:     cfg,
		log:     log,
		pending: make(map[string]*pendingEgress),
	}
	go s.sweepLoop(context.Background())
	return s
}

// Handle ingests one egress report.
func (s *Store) Handle(report Report) {
	if report.TraceID == "" || report.Workload == "" || report.Protocol == "" {
		return
	}
	if !roles.IsValid(report.Workload) {
		return
	}
	if len(report.Payload) == 0 || !json.Valid(report.Payload) {
		return
	}

	s.mu.Lock()
	s.evictExpiredLocked()
	s.enforceCapLocked()

	pt, ok := s.pending[report.TraceID]
	if !ok {
		pt = &pendingEgress{
			deadline: time.Now().Add(s.cfg.TraceTTL),
			reports:  make(map[string][]byte),
			protocol: report.Protocol,
		}
		s.pending[report.TraceID] = pt
		s.order = append(s.order, report.TraceID)
		if s.cfg.EgressWait > 0 {
			traceID := report.TraceID
			pt.timer = time.AfterFunc(s.cfg.EgressWait, func() {
				s.onWaitExpired(traceID)
			})
		}
	}
	if pt.protocol == "" {
		pt.protocol = report.Protocol
	}
	pt.reports[report.Workload] = append([]byte(nil), report.Payload...)

	allPresent := len(pt.reports) >= len(roles.All)
	for _, role := range roles.All {
		if _, ok := pt.reports[role]; !ok {
			allPresent = false
			break
		}
	}
	ready := allPresent && !pt.diffDone
	s.mu.Unlock()

	if ready {
		s.tryDiff(report.TraceID)
	}
}

func (s *Store) onWaitExpired(traceID string) {
	s.mu.Lock()
	pt, ok := s.pending[traceID]
	if !ok || pt.diffDone {
		s.mu.Unlock()
		return
	}
	count := len(pt.reports)
	ready := count >= 2
	s.mu.Unlock()
	if ready {
		s.tryDiff(traceID)
	}
}

func (s *Store) tryDiff(traceID string) {
	s.mu.Lock()
	pt, ok := s.pending[traceID]
	if !ok || pt.diffDone {
		s.mu.Unlock()
		return
	}
	allPresent := true
	for _, role := range roles.All {
		if _, ok := pt.reports[role]; !ok {
			allPresent = false
			break
		}
	}
	if !allPresent && len(pt.reports) < 2 {
		s.mu.Unlock()
		return
	}
	pt.diffDone = true
	if pt.timer != nil {
		pt.timer.Stop()
		pt.timer = nil
	}
	protocol := pt.protocol
	bodyA := copyBytes(pt.reports[roles.ControlA])
	bodyB := copyBytes(pt.reports[roles.ControlB])
	bodyC := copyBytes(pt.reports[roles.Candidate])
	delete(s.pending, traceID)
	s.removeFromOrderLocked(traceID)
	s.mu.Unlock()

	go s.runDiff(traceID, protocol, bodyA, bodyB, bodyC)
}

func (s *Store) runDiff(traceID, protocol string, bodyA, bodyB, bodyC []byte) {
	if bodyA == nil {
		s.log.Info("Skipping egress diff without control-a payload", "traceID", traceID, "protocol", protocol)
		return
	}
	diff.AnalyzeEgress(s.log, traceID, protocol, bodyA, bodyB, bodyC)
}

func copyBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	return append([]byte(nil), in...)
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
		if pt.diffDone {
			delete(s.pending, id)
			s.removeFromOrderLocked(id)
			continue
		}
		received, missing := roleSets(pt.reports)
		s.log.Info(fmt.Sprintf(
			"Timed out waiting for Trace %s (%s egress): received %s; missing %s",
			id, pt.protocol, formatRoleList(received), formatRoleList(missing),
		))
		if pt.timer != nil {
			pt.timer.Stop()
		}
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
		s.log.Warn(fmt.Sprintf("Egress diff map full - evicting Trace %s", oldest))
		if pt := s.pending[oldest]; pt != nil && pt.timer != nil {
			pt.timer.Stop()
		}
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

func roleSets(reports map[string][]byte) (received, missing []string) {
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

func formatRoleList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	return "[" + strings.Join(items, ", ") + "]"
}
