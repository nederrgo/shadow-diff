package consumer

import (
	"context"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/shadow-diff/beruclient"

	"github.com/shadow-diff/egress-relay-rabbitmq/internal/config"
	"github.com/shadow-diff/egress-relay-rabbitmq/internal/firehose"
)

// Runner consumes Firehose events from one shadow broker and forwards them to Beru.
type Runner struct {
	Workload       string
	URL            string
	Beru           *beruclient.Client
	ShadowTestName string
	EgressExchange string
	MinDelay       time.Duration
	MaxDelay       time.Duration
	dedup          *publishDedup
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
			slog.Warn("broker session ended", "workload", r.Workload, "err", err, "reconnect_in", delay)
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
	if !shouldReportEgress(r.EgressExchange, firehose.ExchangeNameFromPublish(msg.Headers, msg.RoutingKey)) {
		return
	}

	traceID, spanID, err := firehose.TraceContextFromFirehose(msg.Headers)
	if err != nil {
		slog.Warn("skipping firehose message with invalid trace context",
			"workload", r.Workload, "routing_key", msg.RoutingKey, "err", err)
		return
	}
	payload, err := firehose.BeruEgressPayload(msg.Headers, msg.RoutingKey, msg.Body)
	if err != nil {
		slog.Warn("skipping invalid firehose message", "workload", r.Workload, "trace_id", traceID, "err", err)
		return
	}
	if r.dedup != nil && !r.dedup.shouldForward(traceID, spanID, msg.Body) {
		slog.Info("discarding duplicate firehose message", "workload", r.Workload, "trace_id", traceID, "span_id", spanID)
		return
	}
	report := beruclient.Report{
		TraceID:        traceID,
		Workload:       r.Workload,
		Protocol:       "rabbitmq",
		Payload:        payload,
		ShadowTestName: r.ShadowTestName,
	}
	if err := r.Beru.PostReport(ctx, report); err != nil {
		slog.Error("Beru post failed", "workload", r.Workload, "trace_id", traceID, "err", err)
	}
}

// StartAll launches one reconnect loop per configured broker URL.
func StartAll(ctx context.Context, cfg config.Config, beruClient *beruclient.Client) {
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
		// Each workload gets its own dedup map so that identical messages from
		// different shadow brokers (same trace+span+payload) don't cross-block
		// each other — dedup only filters the Firehose's own double-delivery.
		dedup := newPublishDedup()
		dedup.startPruner(ctx)
		runner := &Runner{
			Workload:       w.workload,
			URL:            w.url,
			Beru:           beruClient,
			ShadowTestName: cfg.ShadowTestName,
			EgressExchange: cfg.EgressExchange,
			MinDelay:       cfg.ReconnectMin,
			MaxDelay:       cfg.ReconnectMax,
			dedup:          dedup,
		}
		go func() {
			if err := runner.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("runner stopped", "workload", w.workload, "err", err)
			}
		}()
	}
}
