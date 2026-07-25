package export

import (
	"log/slog"
	"sort"
	"sync"
)

// Route is the per-ShadowTest forward target for a captured destination IP.
type Route struct {
	IgrisBaseURL     string
	SamplePercentage int
	RuleKey          string // namespace/name — conflict tie-break
}

// RuleExport is one KaiselRule's export fields for Router.Rebuild.
type RuleExport struct {
	Key              string // namespace/name
	IPs              []string
	IgrisBaseURL     string
	SamplePercentage int
}

// Router maps destination pod IPv4 → igris route.
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

// Lookup returns the route for a destination IP, if any.
func (r *Router) Lookup(dstIP string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.byIP[dstIP]
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
		if rule.IgrisBaseURL == "" {
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
				SamplePercentage: sample,
				RuleKey:          rule.Key,
			}
		}
	}

	r.mu.Lock()
	r.byIP = next
	r.mu.Unlock()
}
