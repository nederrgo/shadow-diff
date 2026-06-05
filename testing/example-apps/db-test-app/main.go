package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultRedisHost = "localhost:6379"

type storeRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type storeResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type getResponse struct {
	Value string `json:"value"`
}

func main() {
	addr := envOr("LISTEN_ADDR", ":8080")
	redisAddr := envOr("REDIS_HOST", defaultRedisHost)

	rdb, err := newRedisClient(redisAddr)
	if err != nil {
		log.Fatalf("redis client: %v", err)
	}
	defer rdb.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /store", handleStore(rdb))
	mux.HandleFunc("GET /store/{key}", handleGet(rdb))

	log.Printf("db-test-app listening on %s redis=%s", addr, redisAddr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func newRedisClient(addr string) (*redis.Client, error) {
	host, port, err := splitHostPort(addr)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", host, port),
	}), nil
}

func splitHostPort(addr string) (host, port string, err error) {
	if strings.Contains(addr, "://") {
		return "", "", fmt.Errorf("URI schemes not supported in REDIS_HOST; use host:port")
	}
	host, port, err = net.SplitHostPort(addr)
	if err != nil {
		if !strings.Contains(addr, ":") {
			return addr, "6379", nil
		}
		return "", "", err
	}
	if port == "" {
		port = "6379"
	}
	return host, port, nil
}

func handleStore(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req storeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if req.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := rdb.Set(ctx, req.Key, req.Value, 0).Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(storeResponse{Key: req.Key, Value: req.Value})
	}
}

func handleGet(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		val, err := rdb.Get(ctx, key).Result()
		if err == redis.Nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(getResponse{Value: val})
	}
}
