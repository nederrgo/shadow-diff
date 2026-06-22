package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestInferDriver(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{Spec: enginev1alpha1.ShadowTestSpec{ServicePort: 3000}}

	cases := []struct {
		port int32
		want string
	}{
		{3000, "http_request"},
		{80, "http_request"},
		{443, "http_request"},
		{8080, "http_request"},
		{27017, "tcp_stream"},
		{6379, "tcp_stream"},
	}
	for _, tc := range cases {
		if got := inferDriver(st, tc.port); got != tc.want {
			t.Fatalf("port %d: got %q want %q", tc.port, got, tc.want)
		}
	}
}

func TestResolvedInputsInfersDriver(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort: 3000,
			Inputs: []enginev1alpha1.InputSpec{
				{Port: 3000},
				{Port: 27017},
			},
		},
	}
	inputs := resolvedInputs(st)
	if inputs[0].Driver != "http_request" || inputs[1].Driver != "tcp_stream" {
		t.Fatalf("got %+v", inputs)
	}
}

func TestResolvedInputsLegacyAddon(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort: 80,
			Inputs:      []enginev1alpha1.InputSpec{{Port: 80, Addon: "http"}},
		},
	}
	if got := resolvedInputs(st)[0].Driver; got != "http_request" {
		t.Fatalf("got %q", got)
	}
}

func TestShadowServicePorts(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort: 80,
			Inputs: []enginev1alpha1.InputSpec{
				{Port: 80, Driver: "http_request"},
				{Port: 27017, Driver: "tcp_stream"},
			},
		},
	}
	ports := shadowServicePorts(st)
	if len(ports) != 2 {
		t.Fatalf("got %d ports", len(ports))
	}
	names := map[string]bool{}
	for _, p := range ports {
		names[p.Name] = true
		if p.Port == 27017 && p.TargetPort.IntVal != 27017 {
			t.Fatalf("27017 target port %+v", p.TargetPort)
		}
	}
	if !names["ingress"] || !names[inputPortName("tcp_stream", 27017)] {
		t.Fatalf("names %v", names)
	}
}

func TestValidateInputsAMQPOnly(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
			}},
			Inputs: []enginev1alpha1.InputSpec{{
				Driver: "rabbitmq_message",
				AMQP: &enginev1alpha1.AMQPInputSpec{
					ProdURL: "amqp://prod:5672", Exchange: "orders", RoutingKey: "order.created",
					TargetDependency: "rabbitmq",
				},
			}},
		},
	}
	if err := validateInputs(st); err != nil {
		t.Fatalf("validateInputs: %v", err)
	}
	if !isAMQPOnlyShadowTest(st) {
		t.Fatal("expected AMQP-only")
	}
}

func TestValidateInputsMixedDriversRejected(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort: 80,
			Inputs: []enginev1alpha1.InputSpec{
				{Driver: "rabbitmq_message", AMQP: &enginev1alpha1.AMQPInputSpec{
					ProdURL: "amqp://x", Exchange: "e", RoutingKey: "k", TargetDependency: "rabbitmq",
				}},
				{Port: 80, Driver: "http_request"},
			},
		},
	}
	if err := validateInputs(st); err == nil {
		t.Fatal("expected error for mixed drivers")
	}
}

func TestBuildPixieStreamRuleSpecAMQPOnlyNoIngressPorts(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
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
	dep := &appsv1.Deployment{}
	spec := buildPixieStreamRuleSpec(st, "shadow-ns", dep)
	if len(spec.TargetPorts) != 0 {
		t.Fatalf("expected no ingress ports, got %v", spec.TargetPorts)
	}
}
