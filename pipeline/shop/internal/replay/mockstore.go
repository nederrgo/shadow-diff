package replay

import "sync"

// MockStore holds egress replay responses keyed by trace-based key.
type MockStore struct {
	mu   sync.RWMutex
	data map[string]EarlyResponse
}

func NewMockStore() *MockStore {
	return &MockStore{data: make(map[string]EarlyResponse)}
}

func (s *MockStore) Put(hash string, resp EarlyResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Don't overwrite a successful (2xx) mock with a non-2xx. With transparent proxy,
	// shadow workers connect to real IPs before iptables intercepts them; Pixie captures
	// those connections and the Recorder may record a 599 (Shop miss) for the same trace
	// key. The real service response (2xx from the prod worker) must win.
	if existing, ok := s.data[hash]; ok && existing.StatusCode >= 200 && existing.StatusCode < 300 {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return
		}
	}
	s.data[hash] = resp
}

func (s *MockStore) Get(hash string) (EarlyResponse, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resp, ok := s.data[hash]
	return resp, ok
}
