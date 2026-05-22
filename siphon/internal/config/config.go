package config

import (
	"fmt"
	"sync"
)

type SiphonListener struct {
	Port   int    `json:"port"`
	Driver string `json:"driver"`
}

type SiphonTarget struct {
	ShadowTest  string           `json:"shadowtest"`
	TargetIPs   []string         `json:"target_ips"`
	TargetPorts []int            `json:"target_ports"`
	IgrisHost   string           `json:"igris_host"`
	Listeners   []SiphonListener `json:"listeners"`
}

type SiphonConfig struct {
	SampleRate int            `json:"sample_rate"`
	Targets    []SiphonTarget `json:"targets"`
}

type Manager struct {
	mu          sync.RWMutex
	cfg         SiphonConfig
	targetIPs   map[string]bool
	targetPorts map[int]bool
	targetMap   map[string]*SiphonTarget // key: "IP:Port"
	portDrivers map[string]string        // key: "IP:Port" -> driver (e.g. "http_request")
}

func NewManager() *Manager {
	return &Manager{
		targetIPs:   make(map[string]bool),
		targetPorts: make(map[int]bool),
		targetMap:   make(map[string]*SiphonTarget),
		portDrivers: make(map[string]string),
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

	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		for _, ip := range t.TargetIPs {
			m.targetIPs[ip] = true
			for _, port := range t.TargetPorts {
				m.targetPorts[port] = true
				key := fmt.Sprintf("%s:%d", ip, port)
				m.targetMap[key] = t

				// Map to driver
				driver := "tcp_stream" // default fallback
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

func (m *Manager) HasAnyTargets() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.targetMap) > 0
}
