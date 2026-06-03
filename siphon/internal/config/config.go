package config

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

type SiphonListener struct {
	Port   int    `json:"port"`
	Driver string `json:"driver"`
}

type SiphonDownstream struct {
	Host        string   `json:"host"`
	IgnorePaths []string `json:"ignore_paths,omitempty"`
}

type SiphonTarget struct {
	ShadowTest   string             `json:"shadowtest"`
	TargetIPs    []string           `json:"target_ips"`
	TargetPorts  []int              `json:"target_ports"`
	IgrisHost    string             `json:"igris_host"`
	Listeners    []SiphonListener   `json:"listeners"`
	BeruHTTPHost string             `json:"beru_http_host"`
	Downstreams  []SiphonDownstream `json:"downstreams,omitempty"`
	ExcludeIPs   []string           `json:"exclude_ips,omitempty"`
}

type SiphonConfig struct {
	SampleRate int            `json:"sample_rate"`
	Targets    []SiphonTarget `json:"targets"`
}

type Manager struct {
	mu           sync.RWMutex
	cfg          SiphonConfig
	targetIPs    map[string]bool
	targetPorts  map[int]bool
	targetMap    map[string]*SiphonTarget // key: "IP:Port"
	portDrivers  map[string]string        // key: "IP:Port" -> driver (e.g. "http_request")
	prodIPTarget map[string]*SiphonTarget // prod pod IP -> target
	excludeIPs   map[string]bool
}

func NewManager() *Manager {
	return &Manager{
		targetIPs:    make(map[string]bool),
		targetPorts:  make(map[int]bool),
		targetMap:    make(map[string]*SiphonTarget),
		portDrivers:  make(map[string]string),
		prodIPTarget: make(map[string]*SiphonTarget),
		excludeIPs:   make(map[string]bool),
	}
}

func (m *Manager) Update(cfg SiphonConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg
	m.targetIPs = make(map[string]bool)
	m.targetPorts = make(map[int]bool)
	m.targetMap = make(map[string]*SiphonTarget)
	m.portDrivers = make(map[string]string)
	m.prodIPTarget = make(map[string]*SiphonTarget)
	m.excludeIPs = make(map[string]bool)

	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		for _, ip := range t.ExcludeIPs {
			m.excludeIPs[ip] = true
		}
		for _, ip := range t.TargetIPs {
			m.targetIPs[ip] = true
			m.prodIPTarget[ip] = t
			for _, port := range t.TargetPorts {
				m.targetPorts[port] = true
				key := fmt.Sprintf("%s:%d", ip, port)
				m.targetMap[key] = t

				driver := "tcp_stream"
				for _, l := range t.Listeners {
					if l.Port == port {
						driver = l.Driver
						break
					}
				}
				m.portDrivers[key] = driver
			}
		}
	}
}

func (m *Manager) GetConfig() SiphonConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) IsTarget(ip string, port int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := fmt.Sprintf("%s:%d", ip, port)
	_, ok := m.targetMap[key]
	return ok
}

func (m *Manager) IsIngressPort(ip string, port int) bool {
	return m.IsTarget(ip, port)
}

func (m *Manager) IsProdPodIP(ip string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.targetIPs[ip]
}

func (m *Manager) LookupTarget(ip string, port int) (*SiphonTarget, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := fmt.Sprintf("%s:%d", ip, port)
	t, ok := m.targetMap[key]
	if !ok {
		return nil, "", false
	}
	return t, m.portDrivers[key], true
}

func (m *Manager) LookupTargetByProdIP(ip string) (*SiphonTarget, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.prodIPTarget[ip]
	return t, ok
}

func (m *Manager) isExcludedIP(ip string) bool {
	return m.excludeIPs[ip]
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return strings.ToLower(h)
	}
	return host
}

func hostMatchesDownstream(host string, downstreams []SiphonDownstream) bool {
	host = normalizeHost(host)
	for _, d := range downstreams {
		dh := normalizeHost(d.Host)
		if dh == host {
			return true
		}
		if strings.HasPrefix(dh, "*.") {
			suffix := strings.TrimPrefix(dh, "*")
			if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(dh, "*.") {
				return true
			}
		}
	}
	return false
}

func (m *Manager) ShouldRecordEgress(srcIP, dstIP string, dstPort int, httpHost string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.targetIPs[srcIP] {
		return false
	}
	if m.isExcludedIP(dstIP) {
		return false
	}
	key := fmt.Sprintf("%s:%d", dstIP, dstPort)
	if _, isIngress := m.targetMap[key]; isIngress {
		return false
	}
	t, ok := m.prodIPTarget[srcIP]
	if !ok || len(t.Downstreams) == 0 {
		return false
	}
	if httpHost != "" && !hostMatchesDownstream(httpHost, t.Downstreams) {
		return false
	}
	return true
}

func (m *Manager) ShouldRecordEgressResponse(srcIP, dstIP string, dstPort int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.targetIPs[dstIP] {
		return false
	}
	if m.isExcludedIP(srcIP) {
		return false
	}
	key := fmt.Sprintf("%s:%d", dstIP, dstPort)
	if _, isIngress := m.targetMap[key]; isIngress {
		return false
	}
	t, ok := m.prodIPTarget[dstIP]
	if !ok || len(t.Downstreams) == 0 {
		return false
	}
	return true
}

func (m *Manager) IgnorePathsForHost(host string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	host = normalizeHost(host)
	for i := range m.cfg.Targets {
		for _, d := range m.cfg.Targets[i].Downstreams {
			if normalizeHost(d.Host) == host {
				return d.IgnorePaths
			}
		}
	}
	return nil
}

func (m *Manager) HasAnyTargets() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.targetMap) > 0
}
