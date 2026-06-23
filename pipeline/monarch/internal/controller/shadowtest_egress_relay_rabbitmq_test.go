package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestEgressRelayRabbitMQDeploymentName(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{ObjectMeta: metav1.ObjectMeta{Name: "rmq-test-shadow"}}
	if got := egressRelayRabbitMQDeploymentName(st); got != "rmq-test-shadow-egress-relay-rabbitmq" {
		t.Fatalf("deployment name = %q, want rmq-test-shadow-egress-relay-rabbitmq", got)
	}
}

func TestEgressRelayRabbitMQImageFor(t *testing.T) {
	t.Setenv("MONARCH_MODE", "")
	t.Setenv(envEgressRelayRabbitMQImage, "")

	st := &enginev1alpha1.ShadowTest{}
	if got := egressRelayRabbitMQImageFor(st); got != "egress-relay-rabbitmq:latest" {
		t.Fatalf("default image = %q", got)
	}
	custom := "egress-relay-rabbitmq:dev"
	st.Spec.EgressRelayRabbitMQ = &enginev1alpha1.EgressRelayRabbitMQSpec{Image: custom}
	if got := egressRelayRabbitMQImageFor(st); got != custom {
		t.Fatalf("custom image = %q", got)
	}
}

func TestEgressRelayRabbitMQEnv(t *testing.T) {
	r := &ShadowTestReconciler{}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "rmq-test", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			Inputs: []enginev1alpha1.InputSpec{{
				Driver: "rabbitmq_message",
				AMQP: &enginev1alpha1.AMQPInputSpec{
					ProdURL: "amqp://prod:5672", Exchange: "orders", RoutingKey: "k",
					TargetDependency: "rabbitmq",
				},
			}},
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
			}},
		},
	}
	env, err := r.egressRelayRabbitMQEnv(st, "shadow-default-rmq-test")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, e := range env {
		byName[e.Name] = e.Value
	}
	if byName[envControlAAMQPURL] == "" || byName[envControlBAMQPURL] == "" || byName[envCandidateAMQPURL] == "" {
		t.Fatalf("missing AMQP URLs: %#v", byName)
	}
	if byName[envBeruHTTPURL] != "http://beru.beru-system.svc.cluster.local:8080" {
		t.Fatalf("BERU_HTTP_URL = %q", byName[envBeruHTTPURL])
	}
}

func TestEgressRelayRabbitMQEnv_HTTPIngressOnly(t *testing.T) {
	r := &ShadowTestReconciler{}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "http-rmq-test", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			Inputs: []enginev1alpha1.InputSpec{{
				Port:   8888,
				Driver: "http_request",
			}},
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
			}},
		},
	}
	env, err := r.egressRelayRabbitMQEnv(st, "shadow-default-http-rmq-test")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, e := range env {
		byName[e.Name] = e.Value
	}
	if byName[envControlAAMQPURL] == "" || byName[envControlBAMQPURL] == "" || byName[envCandidateAMQPURL] == "" {
		t.Fatalf("missing AMQP URLs: %#v", byName)
	}
	if byName["EGRESS_EXCHANGE"] != "egress-events" {
		t.Fatalf("EGRESS_EXCHANGE = %q", byName["EGRESS_EXCHANGE"])
	}
}

func TestNeedsEgressRelayRabbitMQ(t *testing.T) {
	httpRMQ := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Inputs: []enginev1alpha1.InputSpec{{Port: 8888, Driver: "http_request"}},
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
			}},
		},
	}
	if !needsEgressRelayRabbitMQ(httpRMQ) {
		t.Fatal("expected HTTP + rabbitmq dependency to need egress-relay")
	}
	if needsEgressRelayRabbitMQ(&enginev1alpha1.ShadowTest{}) {
		t.Fatal("expected empty ShadowTest to not need egress-relay")
	}
}
