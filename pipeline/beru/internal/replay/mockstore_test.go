package replay

import (
	"sync"
	"testing"
)

func TestMockStore_concurrent(t *testing.T) {
	s := NewMockStore()
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%26))
			s.Put(key, EarlyResponse{StatusCode: 200, Body: []byte("ok")})
		}(i)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%26))
			s.Get(key)
		}(i)
	}
	wg.Wait()
	resp, ok := s.Get("a")
	if !ok || resp.StatusCode != 200 {
		t.Fatalf("expected stored response, got ok=%v resp=%+v", ok, resp)
	}
}
