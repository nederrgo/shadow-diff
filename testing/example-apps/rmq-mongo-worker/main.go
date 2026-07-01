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
	servicePort := envOr("SERVICE_PORT", "8888")
	amqpURL := normalizeAMQPURL(strings.TrimSpace(os.Getenv("AMQP_URL")))
	if amqpURL == "" {
		log.Fatal("AMQP_URL is required")
	}
	mongoURL := envOr("MONGO_URL", "mongodb://127.0.0.1:27017")
	mongoDB := envOr("MONGO_DB", "test")
	exchange := envOr("AMQP_EXCHANGE", "orders")
	queue := envOr("AMQP_QUEUE", "orders")
	routingKey := envOr("AMQP_BINDING_KEY", "order.created")
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /work", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	go func() {
		if err := runConsumer(amqpURL, exchange, queue, routingKey, servicePort, egressExchange, egressRoutingKey, mongoColl); err != nil {
			log.Fatalf("consumer: %v", err)
		}
	}()

	log.Printf("rmq-mongo-worker listen=%s amqp=%s mongo=%s exchange=%s queue=%s egress=%s",
		listen, amqpURL, mongoURL, exchange, queue, egressExchange)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

func runConsumer(amqpURL, exchange, queue, bindingKey, servicePort, egressExchange, egressRoutingKey string, mongoColl *mongo.Collection) error {
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("exchange declare: %w", err)
	}
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("queue declare: %w", err)
	}
	if err := ch.QueueBind(queue, bindingKey, exchange, false, nil); err != nil {
		return fmt.Errorf("queue bind: %w", err)
	}

	deliveries, err := ch.Consume(queue, "rmq-mongo-worker", true, false, false, false, nil)
	if err != nil {
		return err
	}

	ingressClient := &http.Client{Timeout: 30 * time.Second}

	for msg := range deliveries {
		traceID, tp := shadowTraceFromAMQP(msg.Headers)
		if traceID == "" {
			traceID = "missing-trace"
		}
		orderID := parseOrderID(msg.Body)

		log.Printf("consumed routing_key=%s trace=%s order_id=%s", msg.RoutingKey, traceID, orderID)

		doc := bson.M{
			"order_id": orderID,
			"status":   "processed",
		}
		insertOpts := options.InsertOne()
		if tp != "" {
			// ponytail: command $comment (not a document field) for Phase 2b mongo wire scrape
			insertOpts.SetComment(tp)
		}
		if _, err := mongoColl.InsertOne(context.Background(), doc, insertOpts); err != nil {
			log.Printf("mongo insert failed: %v trace=%s", err, traceID)
			continue
		}
		log.Printf("mongo insert ok trace=%s", traceID)

		tpOut := tp
		if tpOut == "" {
			tpOut = formatTraceparent(traceID, randomSpanID())
		}

		ingressURL := fmt.Sprintf("http://127.0.0.1:%s/work", servicePort)
		req, _ := http.NewRequest(http.MethodPost, ingressURL, strings.NewReader(`{"ok":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(headerShadowTraceID, traceID)
		req.Header.Set(headerTraceparent, tpOut)
		if resp, err := ingressClient.Do(req); err != nil {
			log.Printf("ingress via envoy failed: %v trace=%s", err, traceID)
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			log.Printf("ingress report status=%d trace=%s", resp.StatusCode, traceID)
		}

		if egressExchange == "" {
			continue
		}
		if err := publishRabbitMQEgress(ch, egressExchange, egressRoutingKey, traceID, tpOut); err != nil {
			log.Printf("rmq egress publish failed: %v trace=%s", err, traceID)
			continue
		}
		log.Printf("rmq egress published exchange=%s routing_key=%s trace=%s", egressExchange, egressRoutingKey, traceID)
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

func publishRabbitMQEgress(ch *amqp.Channel, exchange, routingKey, traceID, traceparent string) error {
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("egress exchange declare: %w", err)
	}
	body, err := json.Marshal(map[string]string{
		"order_id": "e2e",
		"status":   "shipped",
	})
	if err != nil {
		return err
	}
	return ch.Publish(exchange, routingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Headers: amqp.Table{
			headerShadowTraceID: traceID,
			headerTraceparent:   traceparent,
		},
		Body:         body,
		DeliveryMode: amqp.Persistent,
	})
}

func shadowTraceFromAMQP(h amqp.Table) (traceID, traceparent string) {
	if h == nil {
		return "", ""
	}
	if id := headerString(h, headerShadowTraceID); id != "" && isHexTraceID(id) {
		traceID = strings.ToLower(id)
	}
	tp := headerString(h, headerTraceparent)
	if traceID == "" && tp != "" {
		if tid, ok := parseTraceparent(tp); ok {
			traceID = tid
		}
	}
	return traceID, tp
}

func headerString(h amqp.Table, key string) string {
	v, ok := h[key]
	if !ok {
		return ""
	}
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case []byte:
		return strings.TrimSpace(string(s))
	default:
		return ""
	}
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
