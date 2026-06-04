package main

import (
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
	exchange := envOr("AMQP_EXCHANGE", "orders")
	queue := envOr("AMQP_QUEUE", "worker-queue")
	routingKey := envOr("AMQP_BINDING_KEY", "order.created")
	egressHost := envOr("EGRESS_HOST", "httpbin.org")
	egressPath := envOr("EGRESS_PATH", "/get")
	manualTrace := envOr("RMQ_WORKER_MANUAL_TRACE", "1") != "0"

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
		if err := runConsumer(amqpURL, exchange, queue, routingKey, servicePort, egressHost, egressPath, manualTrace); err != nil {
			log.Fatalf("consumer: %v", err)
		}
	}()

	log.Printf("rmq-test-worker listen=%s amqp=%s exchange=%s queue=%s manual_trace=%v",
		listen, amqpURL, exchange, queue, manualTrace)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

func runConsumer(amqpURL, exchange, queue, bindingKey, servicePort, egressHost, egressPath string, manualTrace bool) error {
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

	deliveries, err := ch.Consume(queue, "rmq-test-worker", true, false, false, false, nil)
	if err != nil {
		return err
	}

	ingressClient := &http.Client{Timeout: 30 * time.Second}
	egressClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}

	for msg := range deliveries {
		traceID, tp := shadowTraceFromAMQP(msg.Headers)
		if traceID == "" {
			traceID = "missing-trace"
		}
		if !manualTrace {
			log.Printf("consumed routing_key=%s trace=%s traceparent_present=%v otel_propagation_mode",
				msg.RoutingKey, traceID, tp != "")
			continue
		}

		log.Printf("consumed message routing_key=%s trace=%s traceparent_present=%v",
			msg.RoutingKey, traceID, tp != "")

		setTraceHTTPHeaders := func(req *http.Request) {
			req.Header.Set(headerShadowTraceID, traceID)
			if tp != "" {
				req.Header.Set(headerTraceparent, tp)
			} else {
				req.Header.Set(headerTraceparent, formatTraceparent(traceID, randomSpanID()))
			}
		}

		ingressURL := fmt.Sprintf("http://127.0.0.1:%s/work", servicePort)
		req, _ := http.NewRequest(http.MethodPost, ingressURL, strings.NewReader(`{"ok":true}`))
		req.Header.Set("Content-Type", "application/json")
		setTraceHTTPHeaders(req)
		if resp, err := ingressClient.Do(req); err != nil {
			log.Printf("ingress via envoy failed: %v", err)
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			log.Printf("ingress report status=%d trace=%s", resp.StatusCode, traceID)
		}

		egressURL := fmt.Sprintf("http://%s%s", egressHost, egressPath)
		ereq, _ := http.NewRequest(http.MethodGet, egressURL, nil)
		setTraceHTTPHeaders(ereq)
		if eresp, err := egressClient.Do(ereq); err != nil {
			log.Printf("egress failed: %v", err)
		} else {
			_, _ = io.Copy(io.Discard, eresp.Body)
			eresp.Body.Close()
			log.Printf("egress status=%d trace=%s", eresp.StatusCode, traceID)
		}
	}
	return nil
}

func shadowTraceFromAMQP(h amqp.Table) (traceID, traceparent string) {
	if h == nil {
		return "", ""
	}
	if id := headerString(h, headerShadowTraceID); id != "" {
		traceID = id
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
