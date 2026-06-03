package replay

import "sync"

// MockStore holds egress replay responses keyed by fuzzed hash.
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
	s.data[hash] = resp
}

func (s *MockStore) Get(hash string) (EarlyResponse, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resp, ok := s.data[hash]
	return resp, ok
}
