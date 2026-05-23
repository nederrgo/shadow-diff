package capture

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"

	"github.com/shadow-diff/siphon/internal/config"
)

// BuildBPFFilter constructs a BPF (pcap-compatible) filter string for TCP traffic
// matching the configured IPv4 targets. It filters bidirectionally using 'host <IP> and port <Port>' clauses.
func BuildBPFFilter(cfg config.SiphonConfig) (string, error) {
	if len(cfg.Targets) == 0 {
		return "", errors.New("BPF builder: no targets configured")
	}

	// Deduplicate target IP and port combinations
	type ipPort struct {
		ip   string
		port int
	}
	var pairs []ipPort
	seen := make(map[string]map[int]bool)

	var hasIPv6Logged bool

	for _, target := range cfg.Targets {
		for _, ipStr := range target.TargetIPs {
			parsedIP := net.ParseIP(ipStr)
			if parsedIP == nil {
				log.Printf("BPF builder warning: invalid IP address %q, skipping", ipStr)
				continue
			}
			if parsedIP.To4() == nil {
				if !hasIPv6Logged {
					log.Printf("BPF builder info: skipping IPv6 address %q (IPv4-only BPF filter supported)", ipStr)
					hasIPv6Logged = true
				}
				continue
			}

			if _, ok := seen[ipStr]; !ok {
				seen[ipStr] = make(map[int]bool)
			}

			for _, port := range target.TargetPorts {
				if port <= 0 || port > 65535 {
					log.Printf("BPF builder warning: invalid port %d, skipping", port)
					continue
				}
				if !seen[ipStr][port] {
					seen[ipStr][port] = true
					pairs = append(pairs, ipPort{ip: ipStr, port: port})
				}
			}
		}
	}

	if len(pairs) == 0 {
		return "", errors.New("BPF builder: no valid IPv4 target IP/port pairs found")
	}

	// Sort pairs to ensure deterministic BPF string output (useful for testing and dedup comparison)
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].ip != pairs[j].ip {
			return pairs[i].ip < pairs[j].ip
		}
		return pairs[i].port < pairs[j].port
	})

	// Build clauses
	var clauses []string
	for _, pair := range pairs {
		clauses = append(clauses, fmt.Sprintf("(host %s and port %d)", pair.ip, pair.port))
	}

	filter := fmt.Sprintf("tcp and ( %s )", strings.Join(clauses, " or "))

	// Check string length and log warning if > 8KB
	if len(filter) > 8*1024 {
		log.Printf("BPF builder warning: BPF filter string length (%d bytes) exceeds 8KB; the filter may be too complex for some kernel versions", len(filter))
	}

	return filter, nil
}
