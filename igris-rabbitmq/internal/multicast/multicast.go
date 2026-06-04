package multicast

import (
	"context"
	"fmt"
	"log"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/igris-rabbitmq/internal/config"
	"github.com/shadow-diff/igris-rabbitmq/internal/trace"
)

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

func (p *ShadowPublisher) PublishAll(msg amqp.Delivery, headers amqp.Table) error {
	pub := amqp.Publishing{
		Headers:     headers,
		ContentType: msg.ContentType,
		Body:        msg.Body,
		DeliveryMode: msg.DeliveryMode,
	}
	for i, ch := range p.channels {
		if err := ch.Publish(p.exchange, msg.RoutingKey, false, false, pub); err != nil {
			return fmt.Errorf("publish shadow broker %d: %w", i, err)
		}
	}
	return nil
}

type Runner struct {
	cfg       config.Config
	publisher *ShadowPublisher
	wg        sync.WaitGroup
}

func NewRunner(cfg config.Config) (*Runner, error) {
	pub, err := NewShadowPublisher(cfg)
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, publisher: pub}, nil
}

func (r *Runner) Close() {
	if r.publisher != nil {
		r.publisher.Close()
	}
	r.wg.Wait()
}

func (r *Runner) Run(ctx context.Context) error {
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

	log.Printf("consuming queue %s, publishing to exchange %s", r.cfg.ShadowQueueName, r.cfg.ShadowPublishExchange)

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

func (r *Runner) handleDelivery(msg amqp.Delivery) {
	headers, err := trace.EnsureTraceHeaders(msg.Headers)
	if err != nil {
		log.Printf("trace headers failed: %v", err)
		_ = msg.Nack(false, true)
		return
	}
	if err := r.publisher.PublishAll(msg, headers); err != nil {
		log.Printf("multicast failed: %v", err)
		_ = msg.Nack(false, true)
		return
	}
	if err := msg.Ack(false); err != nil {
		log.Printf("ack failed: %v", err)
	}
}
