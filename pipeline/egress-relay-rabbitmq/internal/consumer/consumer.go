package consumer

import (
	"context"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/egress-relay-rabbitmq/internal/beru"
	"github.com/shadow-diff/egress-relay-rabbitmq/internal/config"
	"github.com/shadow-diff/egress-relay-rabbitmq/internal/firehose"
)

// Runner consumes Firehose events from one shadow broker and forwards them to Beru.
type Runner struct {
	Workload string
	URL      string
	Beru     *beru.Client
	MinDelay time.Duration
	MaxDelay time.Duration
}

// Run blocks until ctx is cancelled, reconnecting on broker failures.
func (r *Runner) Run(ctx context.Context) error {
	delay := r.MinDelay
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := r.runSession(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Printf("workload=%s broker session ended: %v; reconnecting in %s", r.Workload, err, delay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < r.MaxDelay {
			delay *= 2
			if delay > r.MaxDelay {
				delay = r.MaxDelay
			}
		}
	}
}

func (r *Runner) runSession(ctx context.Context) error {
	var conn *amqp.Connection
	var ch *amqp.Channel
	closeResources := func() {
		if ch != nil {
			_ = ch.Close()
			ch = nil
		}
		if conn != nil {
			_ = conn.Close()
			conn = nil
		}
	}
	defer closeResources()

	var err error
	conn, err = amqp.Dial(r.URL)
	if err != nil {
		return err
	}
	ch, err = conn.Channel()
	if err != nil {
		return err
	}

	queue, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		return err
	}
	if err := ch.QueueBind(queue.Name, firehose.PublishBindKey(), firehose.TraceExchange(), false, nil); err != nil {
		return err
	}

	deliveries, err := ch.Consume(queue.Name, "", true, false, false, false, nil)
	if err != nil {
		return err
	}

	connClosed := make(chan *amqp.Error, 1)
	chClosed := make(chan *amqp.Error, 1)
	conn.NotifyClose(connClosed)
	ch.NotifyClose(chClosed)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cerr := <-connClosed:
			if cerr != nil {
				return cerr
			}
			return amqp.ErrClosed
		case cerr := <-chClosed:
			if cerr != nil {
				return cerr
			}
			return amqp.ErrClosed
		case msg, ok := <-deliveries:
			if !ok {
				return amqp.ErrClosed
			}
			r.handleDelivery(ctx, msg)
		}
	}
}

func (r *Runner) handleDelivery(ctx context.Context, msg amqp.Delivery) {
	if !firehose.IsPublishTrace(msg.RoutingKey) {
		return
	}
	_ = firehose.ExchangeNameFromTrace(msg.Headers)

	traceID, err := firehose.TraceIDFromFirehose(msg.Headers)
	if err != nil {
		log.Printf("workload=%s skip firehose message routing_key=%s: %v", r.Workload, msg.RoutingKey, err)
		return
	}
	payload, err := firehose.PayloadJSON(msg.Body)
	if err != nil {
		log.Printf("workload=%s skip firehose message trace=%s: %v", r.Workload, traceID, err)
		return
	}
	report := beru.Report{
		TraceID:  traceID,
		Workload: r.Workload,
		Protocol: "rabbitmq",
		Payload:  payload,
	}
	if err := r.Beru.PostReport(ctx, report); err != nil {
		log.Printf("workload=%s beru post failed trace=%s: %v", r.Workload, traceID, err)
	}
}

// StartAll launches one reconnect loop per configured broker URL.
func StartAll(ctx context.Context, cfg config.Config, beruClient *beru.Client) {
	workers := []struct {
		workload string
		url      string
	}{
		{"control-a", cfg.ControlAURL},
		{"control-b", cfg.ControlBURL},
		{"candidate", cfg.CandidateURL},
	}
	for _, w := range workers {
		w := w
		runner := &Runner{
			Workload: w.workload,
			URL:      w.url,
			Beru:     beruClient,
			MinDelay: cfg.ReconnectMin,
			MaxDelay: cfg.ReconnectMax,
		}
		go func() {
			if err := runner.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("workload=%s runner stopped: %v", w.workload, err)
			}
		}()
	}
}
