package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	headerShadowTraceID = "x-shadow-trace-id"
	headerTraceparent   = "traceparent"
)

func main() {
	listen := envOr("LISTEN_ADDR", ":8080")
	amqpURL := normalizeAMQPURL(strings.TrimSpace(os.Getenv("AMQP_URL")))
	mongoURL := envOr("MONGO_URL", "mongodb://127.0.0.1:27017")
	mongoDB := envOr("MONGO_DB", "test")
	egressExchange := strings.TrimSpace(os.Getenv("RMQ_EGRESS_EXCHANGE"))
	egressRoutingKey := envOr("RMQ_EGRESS_ROUTING_KEY", "order.shipped")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURL))
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() { _ = mongoClient.Disconnect(context.Background()) }()
	mongoColl := mongoClient.Database(mongoDB).Collection("orders")

	var pub amqpPublisher
	if egressExchange != "" {
		if amqpURL == "" {
			log.Fatal("AMQP_URL is required when RMQ_EGRESS_EXCHANGE is set")
		}
		pub, err = newAMQPPublisher(amqpURL)
		if err != nil {
			log.Fatalf("amqp: %v", err)
		}
		defer pub.Close()
	}

	srv := &server{
		mongoColl:        mongoColl,
		pub:              pub,
		egressExchange:   egressExchange,
		egressRoutingKey: egressRoutingKey,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /work", srv.handleWork)

	log.Printf("http-mongo-worker listen=%s mongo=%s egress=%s", listen, mongoURL, egressExchange)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

type server struct {
	mongoColl        *mongo.Collection
	pub              amqpPublisher
	egressExchange   string
	egressRoutingKey string
}

func (s *server) handleWork(w http.ResponseWriter, r *http.Request) {
	traceID, tp := shadowTraceFromHTTP(r.Header)
	if traceID == "" {
		traceID = "missing-trace"
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	orderID := parseOrderID(body)

	log.Printf("http work trace=%s order_id=%s", traceID, orderID)

	if err := s.processOrder(r.Context(), traceID, tp, orderID); err != nil {
		log.Printf("work failed: %v trace=%s", err, traceID)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *server) processOrder(ctx context.Context, traceID, tp, orderID string) error {
	doc := bson.M{
		"order_id": orderID,
		"status":   "processed",
	}
	insertOpts := options.InsertOne()
	if tp != "" {
		insertOpts.SetComment(tp)
	}
	if _, err := s.mongoColl.InsertOne(ctx, doc, insertOpts); err != nil {
		return fmt.Errorf("mongo insert: %w", err)
	}
	log.Printf("mongo insert ok trace=%s", traceID)

	if s.egressExchange == "" || s.pub == nil {
		return nil
	}
	tpOut := tp
	if tpOut == "" {
		tpOut = formatTraceparent(traceID, randomSpanID())
	}
	if err := s.pub.publish(s.egressExchange, s.egressRoutingKey, traceID, tpOut); err != nil {
		return fmt.Errorf("rmq egress: %w", err)
	}
	log.Printf("rmq egress published exchange=%s routing_key=%s trace=%s",
		s.egressExchange, s.egressRoutingKey, traceID)
	return nil
}

type amqpPublisher interface {
	publish(exchange, routingKey, traceID, traceparent string) error
	Close() error
}

type rabbitPublisher struct {
	mu sync.Mutex
	ch *amqp.Channel
}

func newAMQPPublisher(url string) (*rabbitPublisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &rabbitPublisher{ch: ch}, nil
}

func (p *rabbitPublisher) publish(exchange, routingKey, traceID, traceparent string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"order_id": "e2e",
		"status":   "shipped",
	})
	if err != nil {
		return err
	}
	return p.ch.Publish(exchange, routingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Headers: amqp.Table{
			headerShadowTraceID: traceID,
			headerTraceparent:   traceparent,
		},
		Body:         body,
		DeliveryMode: amqp.Persistent,
	})
}

func (p *rabbitPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		return p.ch.Close()
	}
	return nil
}

func parseOrderID(body []byte) string {
	var data struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal(body, &data); err == nil && data.OrderID != "" {
		return data.OrderID
	}
	return "unknown"
}

func shadowTraceFromHTTP(h http.Header) (traceID, traceparent string) {
	if id := strings.TrimSpace(h.Get(headerShadowTraceID)); id != "" && isHexTraceID(id) {
		traceID = strings.ToLower(id)
	}
	tp := strings.TrimSpace(h.Get(headerTraceparent))
	if traceID == "" && tp != "" {
		if tid, ok := parseTraceparent(tp); ok {
			traceID = tid
		}
	}
	return traceID, tp
}

func isHexTraceID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

func parseTraceparent(h string) (traceID string, ok bool) {
	h = strings.TrimSpace(h)
	parts := strings.Split(h, "-")
	if len(parts) != 4 {
		return "", false
	}
	version, tid, sid, flags := parts[0], parts[1], parts[2], parts[3]
	if len(version) != 2 || len(tid) != 32 || len(sid) != 16 || len(flags) != 2 {
		return "", false
	}
	return strings.ToLower(tid), true
}

func formatTraceparent(traceID, spanID string) string {
	return fmt.Sprintf("00-%s-%s-01", traceID, spanID)
}

func randomSpanID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func normalizeAMQPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "amqp://") || strings.HasPrefix(raw, "amqps://") {
		return raw
	}
	return fmt.Sprintf("amqp://guest:guest@%s/", strings.TrimPrefix(raw, "//"))
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
