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
// matching configured IPv4 targets: ingress (host+port) and egress (src host).
func BuildBPFFilter(cfg config.SiphonConfig) (string, error) {
	if len(cfg.Targets) == 0 {
		return "", errors.New("BPF builder: no targets configured")
	}

	type ipPort struct {
		ip   string
		port int
	}
	var ingressPairs []ipPort
	seenIngress := make(map[string]map[int]bool)
	seenProdIPs := make(map[string]bool)
	var prodIPs []string

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

			if !seenProdIPs[ipStr] {
				seenProdIPs[ipStr] = true
				prodIPs = append(prodIPs, ipStr)
			}

			if _, ok := seenIngress[ipStr]; !ok {
				seenIngress[ipStr] = make(map[int]bool)
			}

			for _, port := range target.TargetPorts {
				if port <= 0 || port > 65535 {
					log.Printf("BPF builder warning: invalid port %d, skipping", port)
					continue
				}
				if !seenIngress[ipStr][port] {
					seenIngress[ipStr][port] = true
					ingressPairs = append(ingressPairs, ipPort{ip: ipStr, port: port})
				}
			}
		}
	}

	if len(ingressPairs) == 0 && len(prodIPs) == 0 {
		return "", errors.New("BPF builder: no valid IPv4 target IP/port pairs found")
	}

	sort.Slice(ingressPairs, func(i, j int) bool {
		if ingressPairs[i].ip != ingressPairs[j].ip {
			return ingressPairs[i].ip < ingressPairs[j].ip
		}
		return ingressPairs[i].port < ingressPairs[j].port
	})
	sort.Strings(prodIPs)

	var ingressClauses []string
	for _, pair := range ingressPairs {
		ingressClauses = append(ingressClauses, fmt.Sprintf("(host %s and port %d)", pair.ip, pair.port))
	}

	var egressClauses []string
	for _, ip := range prodIPs {
		egressClauses = append(egressClauses, fmt.Sprintf("(src host %s)", ip))
	}

	var groups []string
	if len(ingressClauses) > 0 {
		groups = append(groups, strings.Join(ingressClauses, " or "))
	}
	if len(egressClauses) > 0 {
		groups = append(groups, strings.Join(egressClauses, " or "))
	}

	filter := fmt.Sprintf("tcp and ( %s )", strings.Join(groups, " or "))

	if len(filter) > 8*1024 {
		log.Printf("BPF builder warning: BPF filter string length (%d bytes) exceeds 8KB; the filter may be too complex for some kernel versions", len(filter))
	}

	return filter, nil
}
