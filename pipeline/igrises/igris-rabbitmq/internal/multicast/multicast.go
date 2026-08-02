package multicast

import (
	"context"
	"fmt"
	"log"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/igris-rabbitmq/internal/amqptrace"
	"github.com/shadow-diff/igris-rabbitmq/internal/capture"
	"github.com/shadow-diff/igris-rabbitmq/internal/config"
	"github.com/shadow-diff/sample"
	"github.com/shadow-diff/trace"
)

type multicastPublisher interface {
	PublishCapture(rec capture.IngressCapture) error
	Close()
}

type ShadowPublisher struct {
	exchange     string
	exchangeType string
	channels     []*amqp.Channel
	conns        []*amqp.Connection
}

func NewShadowPublisher(cfg config.Config) (*ShadowPublisher, error) {
	urls := []string{cfg.ControlAURL, cfg.ControlBURL, cfg.CandidateURL}
	p := &ShadowPublisher{
		exchange:     cfg.ShadowPublishExchange,
		exchangeType: cfg.ShadowPublishExchangeType,
	}
	for i, url := range urls {
		conn, err := amqp.Dial(url)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("dial shadow broker %d: %w", i, err)
		}
		ch, err := conn.Channel()
		if err != nil {
			_ = conn.Close()
			p.Close()
			return nil, fmt.Errorf("channel shadow broker %d: %w", i, err)
		}
		if err := ch.ExchangeDeclare(
			p.exchange,
			p.exchangeType,
			true,  // durable
			false, // auto-delete
			false, // internal
			false, // no-wait
			nil,
		); err != nil {
			_ = ch.Close()
			_ = conn.Close()
			p.Close()
			return nil, fmt.Errorf("exchange declare shadow broker %d: %w", i, err)
		}
		p.conns = append(p.conns, conn)
		p.channels = append(p.channels, ch)
		log.Printf("declared exchange %q type=%s on shadow broker %d", p.exchange, p.exchangeType, i)
	}
	return p, nil
}

func (p *ShadowPublisher) Close() {
	for _, ch := range p.channels {
		_ = ch.Close()
	}
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.channels = nil
	p.conns = nil
}

// shadowPublishExpirationMs bounds how long a mirrored message sits in a shadow queue.
const shadowPublishExpirationMs = "10000"

func (p *ShadowPublisher) PublishCapture(rec capture.IngressCapture) error {
	headers := amqptrace.StringMapToTable(rec.Headers)
	if tp := rec.Traceparent; tp != "" {
		if headers == nil {
			headers = amqp.Table{}
		}
		headers[trace.HeaderTraceparent] = tp
	}
	exchange := rec.Exchange
	if exchange == "" {
		exchange = p.exchange
	}
	pub := amqp.Publishing{
		Headers:      headers,
		ContentType:  rec.ContentType,
		Body:         rec.Body,
		DeliveryMode: rec.DeliveryMode,
		Expiration:   shadowPublishExpirationMs,
	}
	for i, ch := range p.channels {
		if err := ch.Publish(exchange, rec.RoutingKey, false, false, pub); err != nil {
			return fmt.Errorf("publish shadow broker %d: %w", i, err)
		}
	}
	return nil
}

type recordSink interface {
	Add(any) error
}

// RecordRunner consumes the prod shadow queue and buffers captures to S3.
type RecordRunner struct {
	cfg    config.Config
	sink   recordSink
	wg     sync.WaitGroup
}

func NewRecordRunner(cfg config.Config, sink recordSink) *RecordRunner {
	return &RecordRunner{cfg: cfg, sink: sink}
}

func (r *RecordRunner) Close() { r.wg.Wait() }

func (r *RecordRunner) Run(ctx context.Context) error {
	conn, err := amqp.Dial(r.cfg.ProdURL)
	if err != nil {
		return fmt.Errorf("dial prod: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("prod channel: %w", err)
	}
	defer ch.Close()

	if err := ch.Qos(r.cfg.Prefetch, 0, false); err != nil {
		return fmt.Errorf("qos: %w", err)
	}

	deliveries, err := ch.Consume(r.cfg.ShadowQueueName, "igris-rabbitmq", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %q: %w", r.cfg.ShadowQueueName, err)
	}
	log.Printf("record: consuming queue %s → S3", r.cfg.ShadowQueueName)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("consumer channel closed")
			}
			r.wg.Add(1)
			go func(msg amqp.Delivery) {
				defer r.wg.Done()
				r.handleDelivery(msg)
			}(d)
		}
	}
}

func (r *RecordRunner) handleDelivery(msg amqp.Delivery) {
	resolved, ok := amqptrace.TryInbound(msg.Headers)
	if !ok {
		_ = msg.Ack(false)
		return
	}
	if !sample.SampledIn(resolved.TraceID, r.cfg.SamplePercentage) {
		_ = msg.Ack(false)
		return
	}
	headers := amqptrace.StampHeaders(msg.Headers, resolved)
	rec := capture.IngressCapture{
		Traceparent:  resolved.Traceparent,
		TraceID:      resolved.TraceID,
		RoutingKey:   msg.RoutingKey,
		Exchange:     r.cfg.ShadowPublishExchange,
		ContentType:  msg.ContentType,
		DeliveryMode: msg.DeliveryMode,
		Headers:      amqptrace.TableToStringMap(headers),
		Body:         msg.Body,
	}
	if err := r.sink.Add(rec); err != nil {
		log.Printf("s3 capture failed: %v", err)
		_ = msg.Nack(false, true)
		return
	}
	_ = msg.Ack(false)
}

// ReplayPublisher is the fan-out surface used by the replay engine.
type ReplayPublisher struct {
	pub multicastPublisher
}

func NewReplayPublisher(cfg config.Config) (*ReplayPublisher, error) {
	pub, err := NewShadowPublisher(cfg)
	if err != nil {
		return nil, err
	}
	return &ReplayPublisher{pub: pub}, nil
}

func (r *ReplayPublisher) Close() {
	if r.pub != nil {
		r.pub.Close()
	}
}

func (r *ReplayPublisher) Dispatch(_ context.Context, rec capture.IngressCapture) {
	if err := r.pub.PublishCapture(rec); err != nil {
		log.Printf("replay publish failed: %v", err)
	}
}
