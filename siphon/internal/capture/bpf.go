package capture

import (
	"fmt"
	"net"
	"strings"
)

// BuildBPFFilter returns a tcpdump-style filter for traffic to target pod IPs and ports.
func BuildBPFFilter(ips []string, ports []int) (string, error) {
	if len(ips) == 0 {
		return "", fmt.Errorf("no target IPs")
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no target ports")
	}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("invalid IP %q", ip)
		}
	}
	var hostParts []string
	for _, ip := range ips {
		hostParts = append(hostParts, "dst host "+ip)
	}
	var portParts []string
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid port %d", port)
		}
		portParts = append(portParts, fmt.Sprintf("dst port %d", port))
	}
	return fmt.Sprintf("tcp and (%s) and (%s)",
		strings.Join(hostParts, " or "),
		strings.Join(portParts, " or "),
	), nil
}

// BuildKernelBPFFilter matches TCP flows involving target IPs and ports in either direction.
func BuildKernelBPFFilter(ips []string, ports []int) (string, error) {
	if len(ips) == 0 {
		return "", fmt.Errorf("no target IPs")
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no target ports")
	}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("invalid IP %q", ip)
		}
	}
	var hostParts []string
	for _, ip := range ips {
		hostParts = append(hostParts, "host "+ip)
	}
	var portParts []string
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid port %d", port)
		}
		portParts = append(portParts, fmt.Sprintf("port %d", port))
	}
	return fmt.Sprintf("tcp and (%s) and (%s)",
		strings.Join(hostParts, " or "),
		strings.Join(portParts, " or "),
	), nil
}
