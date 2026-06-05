package als

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"

	"github.com/shadow-diff/beru/internal/diff"
	"github.com/shadow-diff/beru/internal/ingest"
	"github.com/shadow-diff/beru/internal/roles"
)

type queryEntry struct {
	connectionID string
	payload      []byte
}

type pendingMongoEgress struct {
	ingressComplete bool
	queries         map[string][]queryEntry
	deadline        time.Time
	diffDone        bool
}

// Store correlates MongoDB egress ALS entries by trace ID, gated on ingress completion.
type Store struct {
	cfg          ingest.Config
	log          *slog.Logger
	pending      map[string]*pendingMongoEgress
	order        []string
	unattributed map[string][]queryEntry
	mu           sync.Mutex
}

func NewStore(log *slog.Logger, cfg ingest.Config) *Store {
	if log == nil {
		log = slog.Default()
	}
	s := &Store{
		cfg:          cfg,
		log:          log,
		pending:      make(map[string]*pendingMongoEgress),
		unattributed: make(map[string][]queryEntry),
	}
	go s.sweepLoop(context.Background())
	return s
}

// NotifyIngressComplete marks trace T ready for mongo egress diff (required gate).
func (s *Store) NotifyIngressComplete(traceID string) {
	if traceID == "" {
		return
	}
	s.mu.Lock()
	pt := s.ensurePendingLocked(traceID)
	pt.ingressComplete = true
	for role, entries := range s.unattributed {
		pt.queries[role] = append(pt.queries[role], entries...)
	}
	s.unattributed = make(map[string][]queryEntry)
	s.mu.Unlock()
	s.tryDiff(traceID)
}

// ActiveTraceForIngress returns the most recent trace awaiting mongo egress diff.
func (s *Store) ActiveTraceForIngress() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		id := s.order[i]
		pt := s.pending[id]
		if pt != nil && pt.ingressComplete && !pt.diffDone {
			return id
		}
	}
	return ""
}

// Handle ingests a TCP access log entry (buffered until ingress completes for that trace).
// streamRole comes from the ALS stream identifier log_name when custom tags are absent.
func (s *Store) Handle(streamRole string, traceID string, entry *accesslogv3.TCPAccessLogEntry) {
	if entry == nil {
		return
	}
	parsed, err := parseTCPEntry(streamRole, entry)
	if err != nil {
		s.log.Debug("Skipping mongo ALS entry", "streamRole", streamRole, "traceID", traceID, "err", err)
		return
	}
	if !roles.IsValid(parsed.role) {
		s.log.Debug("Skipping mongo ALS entry with unknown role", "traceID", traceID, "role", parsed.role)
		return
	}
	qe := queryEntry{connectionID: parsed.connectionID, payload: parsed.query}

	s.mu.Lock()
	if traceID == "" {
		traceID = s.activeTraceLocked()
	}
	if traceID == "" {
		s.unattributed[parsed.role] = append(s.unattributed[parsed.role], qe)
		s.mu.Unlock()
		return
	}
	pt := s.ensurePendingLocked(traceID)
	pt.queries[parsed.role] = append(pt.queries[parsed.role], qe)
	ready := pt.ingressComplete && !pt.diffDone
	s.mu.Unlock()

	if ready {
		s.tryDiff(traceID)
	}
}

func (s *Store) activeTraceLocked() string {
	for i := len(s.order) - 1; i >= 0; i-- {
		id := s.order[i]
		pt := s.pending[id]
		if pt != nil && pt.ingressComplete && !pt.diffDone {
			return id
		}
	}
	return ""
}

func (s *Store) ensurePendingLocked(traceID string) *pendingMongoEgress {
	s.evictExpiredLocked()
	s.enforceCapLocked()
	pt, ok := s.pending[traceID]
	if !ok {
		pt = &pendingMongoEgress{
			queries:  make(map[string][]queryEntry),
			deadline: time.Now().Add(s.cfg.TraceTTL),
		}
		s.pending[traceID] = pt
		s.order = append(s.order, traceID)
	}
	return pt
}

func (s *Store) tryDiff(traceID string) {
	s.mu.Lock()
	pt, ok := s.pending[traceID]
	if !ok || !pt.ingressComplete || pt.diffDone {
		s.mu.Unlock()
		return
	}
	counts := queryCounts(pt.queries)
	if !allRolesPresent(counts) || !matchingCounts(counts) {
		s.mu.Unlock()
		return
	}
	pt.diffDone = true
	snap := snapshotQueries(pt.queries)
	delete(s.pending, traceID)
	s.removeFromOrderLocked(traceID)
	s.mu.Unlock()

	go s.runDiff(traceID, snap)
}

func (s *Store) runDiff(traceID string, queries map[string][]queryEntry) {
	n := len(queries[roles.ControlA])
	if n == 0 {
		s.log.Info("Could not diff mongo egress: no queries for control-a", "traceID", traceID)
		return
	}
	// Compare the concatenated query sequence per role (ordered by arrival).
	bodyA, err := concatQueryPayloads(queries[roles.ControlA])
	if err != nil {
		s.log.Info("Could not diff mongo egress for control-a", "traceID", traceID, "err", err)
		return
	}
	bodyB, err := concatQueryPayloads(queries[roles.ControlB])
	if err != nil {
		s.log.Info("Could not diff mongo egress for control-b", "traceID", traceID, "err", err)
		return
	}
	bodyC, err := concatQueryPayloads(queries[roles.Candidate])
	if err != nil {
		s.log.Info("Could not diff mongo egress for candidate", "traceID", traceID, "err", err)
		return
	}
	diff.AnalyzeMongoEgress(s.log, traceID, bodyA, bodyB, bodyC)
}

func concatQueryPayloads(entries []queryEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("no entries")
	}
	if len(entries) == 1 {
		return entries[0].payload, nil
	}
	var parts []any
	for _, e := range entries {
		var v any
		if err := json.Unmarshal(e.payload, &v); err != nil {
			parts = append(parts, string(e.payload))
		} else {
			parts = append(parts, v)
		}
	}
	return json.Marshal(parts)
}

func queryCounts(queries map[string][]queryEntry) map[string]int {
	out := make(map[string]int, len(queries))
	for role, q := range queries {
		out[role] = len(q)
	}
	return out
}

func allRolesPresent(counts map[string]int) bool {
	for _, r := range roles.All {
		if counts[r] == 0 {
			return false
		}
	}
	return true
}

func matchingCounts(counts map[string]int) bool {
	n := counts[roles.ControlA]
	for _, r := range roles.All {
		if counts[r] != n {
			return false
		}
	}
	return n > 0
}

func snapshotQueries(src map[string][]queryEntry) map[string][]queryEntry {
	out := make(map[string][]queryEntry, len(src))
	for role, entries := range src {
		cp := make([]queryEntry, len(entries))
		copy(cp, entries)
		out[role] = cp
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
		if pt.diffDone {
			delete(s.pending, id)
			s.removeFromOrderLocked(id)
			continue
		}
		if pt.ingressComplete {
			received, missing := mongoRoleSets(pt.queries)
			s.log.Info(fmt.Sprintf(
				"Timed out waiting for Trace %s (mongodb egress): received %s; missing %s",
				id, formatRoleList(received), formatRoleList(missing),
			))
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
		s.log.Warn(fmt.Sprintf("Mongo egress map full - evicting Trace %s", oldest))
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

func mongoRoleSets(queries map[string][]queryEntry) (received, missing []string) {
	have := make(map[string]struct{}, len(queries))
	for role, q := range queries {
		if len(q) > 0 {
			have[role] = struct{}{}
		}
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

func formatRoleList(rr []string) string {
	if len(rr) == 0 {
		return "[]"
	}
	return "[" + strings.Join(rr, ", ") + "]"
}

