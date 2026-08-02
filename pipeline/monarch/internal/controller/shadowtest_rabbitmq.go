package controller

import (
	"context"
	"fmt"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	amqpQueueNamePrefix = "shadow-diff-"

	amqpArgMaxLength = "x-max-length"
	amqpArgOverflow  = "x-overflow"
	amqpArgExpires   = "x-expires"

	// prodShadowQueueExpiresMs is the idle TTL for leaked prod shadow queues
	// (no consumers). Fail-safe if teardown cannot reach the prod broker.
	prodShadowQueueExpiresMs = 600000 // 10 minutes
)

func prodShadowQueueName(st *enginev1alpha1.ShadowTest) string {
	uid := strings.ToLower(string(st.UID))
	return amqpQueueNamePrefix + uid
}

func prodShadowQueueArgs() amqp.Table {
	return amqp.Table{
		amqpArgMaxLength: int32(500),
		amqpArgOverflow:  "drop-head",
		amqpArgExpires:   int32(prodShadowQueueExpiresMs),
	}
}

func amqpExchangeType(spec *enginev1alpha1.AMQPInputSpec) string {
	t := strings.TrimSpace(strings.ToLower(spec.ExchangeType))
	if t == "" {
		return "topic"
	}
	return t
}

func ensureProdExchange(ch *amqp.Channel, spec *enginev1alpha1.AMQPInputSpec) error {
	kind := amqpExchangeType(spec)
	if err := ch.ExchangeDeclare(
		spec.Exchange,
		kind,
		true,  // durable
		false, // autoDelete
		false, // internal
		false, // noWait
		nil,
	); err != nil {
		return fmt.Errorf("exchange declare %q type=%s: %w", spec.Exchange, kind, err)
	}
	return nil
}

// dialAMQP is overridable in tests (ponytail: real broker required for full AMQP E2E).
var dialAMQP = amqp.Dial

// ensureProdShadowQueueDeclared ensures the durable shadow queue exists unbound and
// patches status.amqpQueueName. It does not QueueBind to the production exchange.
func (r *ShadowTestReconciler) ensureProdShadowQueueDeclared(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
) (string, error) {
	if r.ProdQueueEnsureDeclared != nil {
		return r.ProdQueueEnsureDeclared(ctx, st)
	}
	if !hasRabbitMQInput(st) {
		return "", nil
	}
	if st.Status.AmqpQueueName != "" {
		return st.Status.AmqpQueueName, nil
	}

	amqpSpec, err := firstAMQPInput(st)
	if err != nil {
		return "", err
	}
	name := prodShadowQueueName(st)

	conn, err := dialAMQP(amqpSpec.ProdURL)
	if err != nil {
		return "", fmt.Errorf("dial prod broker: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return "", fmt.Errorf("prod broker channel: %w", err)
	}
	defer ch.Close()

	if err := ensureProdExchange(ch, amqpSpec); err != nil {
		return "", err
	}

	if _, err := ch.QueueDeclare(
		name,
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		prodShadowQueueArgs(),
	); err != nil {
		return "", fmt.Errorf("queue declare %q: %w", name, err)
	}

	if err := r.patchAmqpQueueName(ctx, st, name); err != nil {
		return "", fmt.Errorf("patch status amqpQueueName: %w", err)
	}
	return name, nil
}

// ensureProdShadowQueueBound binds the declared shadow queue to the production
// exchange. Safe to call repeatedly (RabbitMQ QueueBind is idempotent).
func (r *ShadowTestReconciler) ensureProdShadowQueueBound(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
) error {
	if r.ProdQueueEnsureBound != nil {
		return r.ProdQueueEnsureBound(ctx, st)
	}
	if !hasRabbitMQInput(st) {
		return nil
	}

	amqpSpec, err := firstAMQPInput(st)
	if err != nil {
		return err
	}
	name := st.Status.AmqpQueueName
	if name == "" {
		name = prodShadowQueueName(st)
	}

	conn, err := dialAMQP(amqpSpec.ProdURL)
	if err != nil {
		return fmt.Errorf("dial prod broker: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("prod broker channel: %w", err)
	}
	defer ch.Close()

	if err := ensureProdExchange(ch, amqpSpec); err != nil {
		return err
	}
	if err := ch.QueueBind(name, amqpSpec.RoutingKey, amqpSpec.Exchange, false, nil); err != nil {
		return fmt.Errorf("queue bind %q: %w", name, err)
	}
	return nil
}

// ensureProdShadowQueue declares then binds. Record mode uses the split
// Declared/Bound helpers; kept for tests and callers that need both steps.
func (r *ShadowTestReconciler) ensureProdShadowQueue(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
) (string, error) {
	name, err := r.ensureProdShadowQueueDeclared(ctx, st)
	if err != nil {
		return "", err
	}
	if err := r.ensureProdShadowQueueBound(ctx, st); err != nil {
		return "", err
	}
	return name, nil
}

func (r *ShadowTestReconciler) patchAmqpQueueName(ctx context.Context, st *enginev1alpha1.ShadowTest, name string) error {
	base := st.DeepCopy()
	st.Status.AmqpQueueName = name
	return r.Status().Patch(ctx, st, client.MergeFrom(base))
}

func (r *ShadowTestReconciler) deleteProdShadowQueue(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	if !hasRabbitMQInput(st) {
		return nil
	}
	queueName := st.Status.AmqpQueueName
	if queueName == "" {
		queueName = prodShadowQueueName(st)
	}

	amqpSpec, err := firstAMQPInput(st)
	if err != nil {
		return err
	}

	conn, err := dialAMQP(amqpSpec.ProdURL)
	if err != nil {
		// broker is gone — queue is gone too, unblock deletion
		log.FromContext(ctx).Info("prod broker unreachable during queue delete, skipping", "err", err)
		return nil
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("prod broker channel for delete: %w", err)
	}
	defer ch.Close()

	if _, err := ch.QueueDelete(queueName, false, false, false); err != nil {
		return fmt.Errorf("queue delete %q: %w", queueName, err)
	}
	return nil
}
