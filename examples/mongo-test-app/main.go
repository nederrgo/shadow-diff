package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

const (
	defaultListenAddr = ":8080"
	defaultMongoURL   = "mongodb://127.0.0.1:27017"
	collectionName    = "test.items"
)

type writeRequest struct {
	Data string `json:"data"`
}

func main() {
	addr := envOr("LISTEN_ADDR", defaultListenAddr)
	mongoURL := envOr("MONGO_URL", defaultMongoURL)
	if strings.Contains(mongoURL, "mongodb+srv") || strings.Contains(strings.ToLower(mongoURL), "tls=true") {
		log.Fatalf("MONGO_URL must be cleartext mongodb:// (got %q)", mongoURL)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /write", handleWrite(mongoURL))

	log.Printf("mongo-test-app listening on %s mongo=%s (legacy OP_INSERT wire protocol)", addr, mongoURL)
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

func handleWrite(mongoURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		traceID := strings.TrimSpace(r.Header.Get("x-shadow-trace-id"))
		if traceID == "" {
			http.Error(w, "x-shadow-trace-id required", http.StatusBadRequest)
			return
		}
		var req writeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		doc, err := bson.Marshal(bson.M{
			"_shadowTraceId": traceID,
			"data":           req.Data,
			"ts":             time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := insertLegacy(mongoURL, collectionName, doc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("trace=%s mongo insert ok", traceID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"traceId": traceID, "status": "ok"})
	}
}
