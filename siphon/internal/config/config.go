package config

import (
	"fmt"
	"net"
	"sync"
)

// Payload is the Monarch POST /v1/config body.
type Payload struct {
	SampleRate int      `json:"sample_rate"`
	Targets    []Target `json:"targets"`
}

// Target is one ShadowTest capture route.
type Target struct {
	ShadowTest  string     `json:"shadowtest"`
	TargetIPs   []string   `json:"target_ips"`
	TargetPorts []int      `json:"target_ports"`
	IgrisHost   string     `json:"igris_host"`
	Listeners   []Listener `json:"listeners"`
}

// Listener is an Igris ingress port and driver.
type Listener struct {
	Port   int    `json:"port"`
	Driver string `json:"driver"`
}

// Store holds the active configuration with atomic swap.
type Store struct {
	mu      sync.RWMutex
	payload Payload
}

func NewStore() *Store {
	return &Store{}
}

func (s *Store) Get() Payload {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.payload
}

func (s *Store) Set(p Payload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payload = p
}

// Validate checks the payload shape.
func (p *Payload) Validate() error {
	if p.SampleRate < 0 || p.SampleRate > 100 {
		return fmt.Errorf("sample_rate must be 0-100")
	}
	for _, t := range p.Targets {
		if t.IgrisHost == "" {
			return fmt.Errorf("target %q: igris_host required", t.ShadowTest)
		}
		for _, ip := range t.TargetIPs {
			if net.ParseIP(ip) == nil {
				return fmt.Errorf("target %q: invalid IP %q", t.ShadowTest, ip)
			}
		}
		for _, port := range t.TargetPorts {
			if port < 1 || port > 65535 {
				return fmt.Errorf("target %q: invalid port %d", t.ShadowTest, port)
			}
		}
	}
	return nil
}

// UnionCaptureTargets returns deduplicated IPs and ports for BPF.
func (p *Payload) UnionCaptureTargets() (ips []string, ports []int) {
	ipSeen := map[string]struct{}{}
	portSeen := map[int]struct{}{}
	for _, t := range p.Targets {
		for _, ip := range t.TargetIPs {
			if _, ok := ipSeen[ip]; !ok {
				ipSeen[ip] = struct{}{}
				ips = append(ips, ip)
			}
		}
		for _, port := range t.TargetPorts {
			if _, ok := portSeen[port]; !ok {
				portSeen[port] = struct{}{}
				ports = append(ports, port)
			}
		}
	}
	return ips, ports
}

// HTTPListenerPorts maps prod dst port -> true when driver is http_request.
func (p Payload) HTTPListenerPorts() map[int]struct{} {
	out := map[int]struct{}{}
	for _, t := range p.Targets {
		http := false
		for _, l := range t.Listeners {
			if l.Driver == "http_request" {
				http = true
				break
			}
		}
		if !http {
			continue
		}
		for _, port := range t.TargetPorts {
			out[port] = struct{}{}
		}
	}
	return out
}

// Route matches a captured flow to its ShadowTest target and listener port on Igris.
type Route struct {
	Target       Target
	IgrisPort    int
	ShadowTestID string
}

// LookupRoute finds the target for dst IP and port.
func (p *Payload) LookupRoute(dstIP string, dstPort int) (Route, bool) {
	for _, t := range p.Targets {
		ipMatch := false
		for _, ip := range t.TargetIPs {
			if ip == dstIP {
				ipMatch = true
				break
			}
		}
		if !ipMatch {
			continue
		}
		portMatch := false
		for _, port := range t.TargetPorts {
			if port == dstPort {
				portMatch = true
				break
			}
		}
		if !portMatch {
			continue
		}
		for _, l := range t.Listeners {
			if l.Driver != "http_request" {
				continue
			}
			// Monarch uses the same port for prod capture and Igris listener.
			if l.Port == dstPort {
				return Route{Target: t, IgrisPort: l.Port, ShadowTestID: t.ShadowTest}, true
			}
		}
	}
	return Route{}, false
}
