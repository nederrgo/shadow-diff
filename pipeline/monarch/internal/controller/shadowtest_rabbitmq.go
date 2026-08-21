package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
	corev1 "k8s.io/api/core/v1"
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

	secretKeyAMQPUsername = "username"
	secretKeyAMQPPassword = "password"
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

// parseHostOnlyAMQPURL requires amqp/amqps with a host and no userinfo.
func parseHostOnlyAMQPURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("amqp.prodUrl is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("amqp.prodUrl: %w", err)
	}
	if u.Scheme != "amqp" && u.Scheme != "amqps" {
		return nil, fmt.Errorf("amqp.prodUrl scheme must be amqp or amqps")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("amqp.prodUrl host is required")
	}
	if u.User != nil {
		return nil, fmt.Errorf("amqp.prodUrl must not include credentials; use credentialsSecretRef")
	}
	return u, nil
}

func amqpCredentialsSecretName(spec *enginev1alpha1.AMQPInputSpec) (string, error) {
	if spec.CredentialsSecretRef == nil {
		return "", fmt.Errorf("amqp.credentialsSecretRef is required")
	}
	name := strings.TrimSpace(spec.CredentialsSecretRef.Name)
	if name == "" {
		return "", fmt.Errorf("amqp.credentialsSecretRef.name is required")
	}
	return name, nil
}

// resolveProdAMQPURL builds a dial DSN from host-only prodUrl plus Secret username/password.
func (r *ShadowTestReconciler) resolveProdAMQPURL(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	spec *enginev1alpha1.AMQPInputSpec,
) (string, error) {
	u, err := parseHostOnlyAMQPURL(spec.ProdURL)
	if err != nil {
		return "", err
	}
	name, err := amqpCredentialsSecretName(spec)
	if err != nil {
		return "", err
	}
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: st.Namespace, Name: name}, &sec); err != nil {
		return "", fmt.Errorf("amqp credentials secret %s/%s: %w", st.Namespace, name, err)
	}
	user := strings.TrimSpace(string(sec.Data[secretKeyAMQPUsername]))
	pass := string(sec.Data[secretKeyAMQPPassword])
	if user == "" {
		return "", fmt.Errorf("amqp credentials secret %s/%s: missing %s", st.Namespace, name, secretKeyAMQPUsername)
	}
	if _, ok := sec.Data[secretKeyAMQPPassword]; !ok {
		return "", fmt.Errorf("amqp credentials secret %s/%s: missing %s", st.Namespace, name, secretKeyAMQPPassword)
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
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

	dsn, err := r.resolveProdAMQPURL(ctx, st, amqpSpec)
	if err != nil {
		return "", err
	}
	conn, err := dialAMQP(dsn)
	if err != nil {
		return "", fmt.Errorf("dial prod broker: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return "", fmt.Errorf("prod broker channel: %w", err)
	}
	defer ch.Close()

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

	dsn, err := r.resolveProdAMQPURL(ctx, st, amqpSpec)
	if err != nil {
		return err
	}
	conn, err := dialAMQP(dsn)
	if err != nil {
		return fmt.Errorf("dial prod broker: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("prod broker channel: %w", err)
	}
	defer ch.Close()

	if err := ch.QueueBind(name, amqpSpec.RoutingKey, amqpSpec.Exchange, false, nil); err != nil {
		return fmt.Errorf("queue bind %q to exchange %q: %w", name, amqpSpec.Exchange, err)
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

	dsn, err := r.resolveProdAMQPURL(ctx, st, amqpSpec)
	if err != nil {
		return err
	}

	conn, err := dialAMQP(dsn)
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
