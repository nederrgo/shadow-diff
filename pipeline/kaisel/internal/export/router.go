package export

import (
	"log/slog"
	"sort"
	"sync"
)

// Route is the per-ShadowTest forward target for a captured target pod IP.
// The same entry serves both directions: matched as a packet's destination it
// is an ingress capture bound for igris, matched as its source it is an egress
// capture bound for that ShadowTest's Shop.
type Route struct {
	IgrisBaseURL     string
	EgressBaseURL    string
	SamplePercentage int
	RuleKey          string // namespace/name — conflict tie-break
}

// RuleExport is one KaiselRule's export fields for Router.Rebuild.
type RuleExport struct {
	Key              string // namespace/name
	IPs              []string
	IgrisBaseURL     string
	EgressBaseURL    string
	SamplePercentage int
}

// Router maps target pod IPv4 → route, for both capture directions.
type Router struct {
	mu   sync.RWMutex
	byIP map[string]Route
	log  *slog.Logger
}

func NewRouter(log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{byIP: make(map[string]Route), log: log}
}

// Lookup returns the route for a target pod IP, if any. Callers pass the
// destination for ingress and the source for egress; one table serves both.
func (r *Router) Lookup(ip string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.byIP[ip]
	return route, ok
}

// Rebuild replaces the IP→route table from the full rule set.
// On IP conflict, the lexicographically first RuleKey wins; losers are warned.
func (r *Router) Rebuild(rules []RuleExport) {
	next := make(map[string]Route)
	// Deterministic: process rules sorted by key so first-wins is stable.
	sorted := append([]RuleExport(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	for _, rule := range sorted {
		// A rule with only one URL set is still useful: it captures that one
		// direction. Only a rule with neither has nowhere to send anything.
		if rule.IgrisBaseURL == "" && rule.EgressBaseURL == "" {
			continue
		}
		sample := rule.SamplePercentage
		if sample <= 0 {
			sample = 100
		}
		for _, ip := range rule.IPs {
			if ip == "" {
				continue
			}
			if prev, ok := next[ip]; ok {
				r.log.Warn("kaisel export IP conflict; keeping lexicographically first rule",
					"ip", ip, "kept", prev.RuleKey, "dropped", rule.Key)
				continue
			}
			next[ip] = Route{
				IgrisBaseURL:     rule.IgrisBaseURL,
				EgressBaseURL:    rule.EgressBaseURL,
				SamplePercentage: sample,
				RuleKey:          rule.Key,
			}
		}
	}

	r.mu.Lock()
	r.byIP = next
	r.mu.Unlock()
}
