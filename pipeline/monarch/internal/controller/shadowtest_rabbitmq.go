package controller

import (
	"context"
	"fmt"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	amqpQueueNamePrefix = "shadow-diff-"

	amqpArgMaxLength = "x-max-length"
	amqpArgOverflow  = "x-overflow"
	amqpArgExpires   = "x-expires"
)

func prodShadowQueueName(st *enginev1alpha1.ShadowTest) string {
	uid := strings.ToLower(string(st.UID))
	return amqpQueueNamePrefix + uid
}

func prodShadowQueueArgs() amqp.Table {
	return amqp.Table{
		amqpArgMaxLength: int32(5000),
		amqpArgOverflow:  "drop-head",
		amqpArgExpires:   int32(86400000),
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

func (r *ShadowTestReconciler) ensureProdShadowQueue(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
) (string, error) {
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

	conn, err := amqp.Dial(amqpSpec.ProdURL)
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
	if err := ch.QueueBind(name, amqpSpec.RoutingKey, amqpSpec.Exchange, false, nil); err != nil {
		return "", fmt.Errorf("queue bind %q: %w", name, err)
	}

	if err := r.patchAmqpQueueName(ctx, st, name); err != nil {
		return "", fmt.Errorf("patch status amqpQueueName: %w", err)
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

	conn, err := amqp.Dial(amqpSpec.ProdURL)
	if err != nil {
		return fmt.Errorf("dial prod broker for queue delete: %w", err)
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

// refreshShadowTest reloads the ShadowTest after status patches (e.g. amqpQueueName).
func (r *ShadowTestReconciler) refreshShadowTest(ctx context.Context, nn types.NamespacedName, st *enginev1alpha1.ShadowTest) error {
	return r.Get(ctx, nn, st)
}
