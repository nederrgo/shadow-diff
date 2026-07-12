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

func TestMockStore_Put_first2xxWins(t *testing.T) {
	s := NewMockStore()
	key := "trace:abc:POST:host:/v1/log"
	s.Put(key, EarlyResponse{StatusCode: 200, Body: []byte("first")})
	s.Put(key, EarlyResponse{StatusCode: 200, Body: []byte("second")})
	resp, ok := s.Get(key)
	if !ok || string(resp.Body) != "first" {
		t.Fatalf("first 2xx must win, got ok=%v body=%q", ok, resp.Body)
	}
}

func TestMockStore_Put_2xxWinsOver599(t *testing.T) {
	s := NewMockStore()
	key := "trace:abc:POST:host:/v1/log"
	s.Put(key, EarlyResponse{StatusCode: 200, Body: []byte("ok")})
	s.Put(key, EarlyResponse{StatusCode: 599, Body: []byte("miss")})
	resp, ok := s.Get(key)
	if !ok || resp.StatusCode != 200 || string(resp.Body) != "ok" {
		t.Fatalf("2xx must beat 599, got ok=%v resp=%+v", ok, resp)
	}
}

func TestMockStore_Put_emptyThen2xx(t *testing.T) {
	s := NewMockStore()
	key := "trace:abc:POST:host:/v1/log"
	s.Put(key, EarlyResponse{StatusCode: 200, Body: []byte("seeded")})
	resp, ok := s.Get(key)
	if !ok || resp.StatusCode != 200 || string(resp.Body) != "seeded" {
		t.Fatalf("empty→2xx must seed, got ok=%v resp=%+v", ok, resp)
	}
}

func TestMockStore_Put_599Then2xx(t *testing.T) {
	s := NewMockStore()
	key := "trace:abc:POST:host:/v1/log"
	s.Put(key, EarlyResponse{StatusCode: 599, Body: []byte("miss")})
	s.Put(key, EarlyResponse{StatusCode: 200, Body: []byte("ok")})
	resp, ok := s.Get(key)
	if !ok || resp.StatusCode != 200 || string(resp.Body) != "ok" {
		t.Fatalf("2xx must overwrite prior 599, got ok=%v resp=%+v", ok, resp)
	}
}
