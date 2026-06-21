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
	"github.com/shadow-diff/beru/internal/storage"
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
	TraceID        string
	Workload       string
	Protocol       string
	Payload        []byte
	ShadowTestName string
}

type pendingEgress struct {
	deadline       time.Time
	payloads       map[string]map[string][][]byte // protocol -> role -> ordered payloads
	diffDone       map[string]bool                // protocol -> diff completed
	timer          *time.Timer
	shadowTestName string
}

// Store correlates egress payloads by trace ID.
type Store struct {
	cfg     Config
	log     *slog.Logger
	pending map[string]*pendingEgress
	order   []string
	mu      sync.Mutex
	Storage *storage.DB
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

	protocol := report.Protocol
	s.mu.Lock()
	s.evictExpiredLocked()
	s.enforceCapLocked()

	pt, ok := s.pending[report.TraceID]
	if !ok {
		pt = &pendingEgress{
			deadline: time.Now().Add(s.cfg.TraceTTL),
			payloads: make(map[string]map[string][][]byte),
			diffDone: make(map[string]bool),
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
	if report.ShadowTestName != "" {
		pt.shadowTestName = report.ShadowTestName
	}
	if pt.payloads[protocol] == nil {
		pt.payloads[protocol] = make(map[string][][]byte)
	}
	pt.payloads[protocol][report.Workload] = append(
		pt.payloads[protocol][report.Workload],
		copyBytes(report.Payload),
	)

	ready := protocolReady(pt, protocol) && !pt.diffDone[protocol]
	s.mu.Unlock()

	if ready {
		if s.cfg.EgressWait > 0 {
			// ponytail: batch OTLP spans until EgressWait; onWaitExpired diffs all ready protocols
			return
		}
		s.tryDiffProtocol(report.TraceID, protocol)
	}
}

func protocolReady(pt *pendingEgress, protocol string) bool {
	byRole := pt.payloads[protocol]
	if byRole == nil {
		return false
	}
	for _, role := range roles.All {
		if len(byRole[role]) == 0 {
			return false
		}
	}
	return true
}

func (s *Store) onWaitExpired(traceID string) {
	s.mu.Lock()
	pt, ok := s.pending[traceID]
	if !ok {
		s.mu.Unlock()
		return
	}
	var protocols []string
	for protocol := range pt.payloads {
		if pt.diffDone[protocol] {
			continue
		}
		if protocolReady(pt, protocol) || protocolHasMinReports(pt, protocol, 2) {
			protocols = append(protocols, protocol)
		}
	}
	s.mu.Unlock()
	for _, protocol := range protocols {
		s.tryDiffProtocol(traceID, protocol)
	}
}

func protocolHasMinReports(pt *pendingEgress, protocol string, min int) bool {
	byRole := pt.payloads[protocol]
	if byRole == nil {
		return false
	}
	count := 0
	for _, role := range roles.All {
		if len(byRole[role]) > 0 {
			count++
		}
	}
	return count >= min
}

func (s *Store) tryDiffProtocol(traceID, protocol string) {
	s.mu.Lock()
	pt, ok := s.pending[traceID]
	if !ok || pt.diffDone[protocol] {
		s.mu.Unlock()
		return
	}
	if !protocolReady(pt, protocol) && !protocolHasMinReports(pt, protocol, 2) {
		s.mu.Unlock()
		return
	}
	pt.diffDone[protocol] = true
	shadowName := pt.shadowTestName
	controlA := copySlice(pt.payloads[protocol][roles.ControlA])
	controlB := copySlice(pt.payloads[protocol][roles.ControlB])
	candidate := copySlice(pt.payloads[protocol][roles.Candidate])

	allDone := true
	for p := range pt.payloads {
		if !pt.diffDone[p] {
			allDone = false
			break
		}
	}
	if allDone {
		if pt.timer != nil {
			pt.timer.Stop()
			pt.timer = nil
		}
		delete(s.pending, traceID)
		s.removeFromOrderLocked(traceID)
	}
	s.mu.Unlock()

	go s.runDiff(traceID, protocol, shadowName, controlA, controlB, candidate)
}

func (s *Store) runDiff(traceID, protocol, shadowTestName string, controlA, controlB, candidate [][]byte) {
	if len(controlA) == 0 {
		s.log.Info("Skipping egress diff without control-a payload", "traceID", traceID, "protocol", protocol)
		return
	}
	var userNoise map[string]struct{}
	shadowName := shadowTestName
	if s.Storage != nil {
		if shadowName == "" {
			shadowName = s.Storage.DefaultShadowTestName()
		}
		var err error
		userNoise, err = s.Storage.NoisePathsForTest(context.Background(), shadowName)
		if err != nil {
			s.log.Error("Could not load noise filters", "traceID", traceID, "err", err)
		}
	}
	res, err := diff.AnalyzeEgress(s.log, traceID, protocol, controlA, controlB, candidate, userNoise)
	if err != nil {
		return
	}
	if s.Storage != nil && res.Err == nil {
		if err := s.Storage.SaveDiffResult(context.Background(), shadowName, res); err != nil {
			s.log.Error("Could not persist diff result", "traceID", traceID, "err", err)
		}
	}
}

func copyBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	return append([]byte(nil), in...)
}

func copySlice(in [][]byte) [][]byte {
	if in == nil {
		return nil
	}
	out := make([][]byte, len(in))
	for i, b := range in {
		out[i] = copyBytes(b)
	}
	return out
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
		if allProtocolsDone(pt) {
			delete(s.pending, id)
			s.removeFromOrderLocked(id)
			continue
		}
		for protocol := range pt.payloads {
			if pt.diffDone[protocol] {
				continue
			}
			received, missing := roleSetsForProtocol(pt, protocol)
			s.log.Info(fmt.Sprintf(
				"Timed out waiting for Trace %s (%s egress): received %s; missing %s",
				id, protocol, formatRoleList(received), formatRoleList(missing),
			))
		}
		if pt.timer != nil {
			pt.timer.Stop()
		}
		delete(s.pending, id)
		s.removeFromOrderLocked(id)
	}
}

func allProtocolsDone(pt *pendingEgress) bool {
	if len(pt.payloads) == 0 {
		return true
	}
	for p := range pt.payloads {
		if !pt.diffDone[p] {
			return false
		}
	}
	return true
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

func roleSetsForProtocol(pt *pendingEgress, protocol string) (received, missing []string) {
	byRole := pt.payloads[protocol]
	for _, r := range roles.All {
		if byRole != nil && len(byRole[r]) > 0 {
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
