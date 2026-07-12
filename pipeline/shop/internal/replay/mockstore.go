package replay

import (
	"log"
	"sync"
)

// MockStore holds egress replay responses keyed by trace-based key.
type MockStore struct {
	mu   sync.RWMutex
	data map[string]EarlyResponse
}

func NewMockStore() *MockStore {
	return &MockStore{data: make(map[string]EarlyResponse)}
}

func is2xx(code int) bool {
	return code >= 200 && code < 300
}

func (s *MockStore) Put(hash string, resp EarlyResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Keep the first 2xx. Dual-side Pixie export can seed the same key twice;
	// transparent-proxy races may also try to overwrite a real 2xx with a 599.
	if existing, ok := s.data[hash]; ok && is2xx(existing.StatusCode) {
		if is2xx(resp.StatusCode) {
			log.Printf("shop mockstore: keep first 2xx key=%s (ignore duplicate seed)", hash)
			return
		}
		log.Printf("shop mockstore: keep 2xx key=%s (ignore status=%d)", hash, resp.StatusCode)
		return
	}
	s.data[hash] = resp
}

func (s *MockStore) Get(hash string) (EarlyResponse, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resp, ok := s.data[hash]
	return resp, ok
}
