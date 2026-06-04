package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

var wellKnownHTTPPorts = map[int32]bool{
	80:   true,
	443:  true,
	8080: true,
}

func inferDriver(st *enginev1alpha1.ShadowTest, port int32) string {
	if port == st.Spec.ServicePort || wellKnownHTTPPorts[port] {
		return "http_request"
	}
	return "tcp_stream"
}

func hasRabbitMQInput(st *enginev1alpha1.ShadowTest) bool {
	for _, in := range st.Spec.Inputs {
		d := strings.TrimSpace(strings.ToLower(in.Driver))
		if d == "rabbitmq_message" {
			return true
		}
	}
	return false
}

func isAMQPOnlyShadowTest(st *enginev1alpha1.ShadowTest) bool {
	if len(st.Spec.Inputs) == 0 {
		return false
	}
	for _, in := range st.Spec.Inputs {
		if strings.TrimSpace(strings.ToLower(in.Driver)) != "rabbitmq_message" {
			return false
		}
	}
	return true
}

func firstAMQPInput(st *enginev1alpha1.ShadowTest) (*enginev1alpha1.AMQPInputSpec, error) {
	for _, in := range st.Spec.Inputs {
		if strings.TrimSpace(strings.ToLower(in.Driver)) == "rabbitmq_message" {
			if in.AMQP == nil {
				return nil, fmt.Errorf("rabbitmq_message input requires amqp configuration")
			}
			return in.AMQP, nil
		}
	}
	return nil, fmt.Errorf("no rabbitmq_message input found")
}

func dependencyByName(st *enginev1alpha1.ShadowTest, name string) (*enginev1alpha1.DependencySpec, bool) {
	want := sanitizeForDNS(name)
	for i := range st.Spec.Dependencies {
		if sanitizeForDNS(st.Spec.Dependencies[i].Name) == want {
			return &st.Spec.Dependencies[i], true
		}
	}
	return nil, false
}

func normalizeInputSpec(st *enginev1alpha1.ShadowTest, in enginev1alpha1.InputSpec) enginev1alpha1.InputSpec {
	d := strings.TrimSpace(strings.ToLower(in.Driver))
	if d == "" {
		a := strings.TrimSpace(strings.ToLower(in.Addon))
		switch a {
		case "http", "http_request":
			d = "http_request"
		case "tcp_stream":
			d = "tcp_stream"
		case "rabbitmq_message":
			d = "rabbitmq_message"
		}
	}
	if d == "rabbitmq_message" {
		return enginev1alpha1.InputSpec{Driver: d, AMQP: in.AMQP}
	}
	if d == "" && in.Port > 0 {
		d = inferDriver(st, in.Port)
	}
	return enginev1alpha1.InputSpec{Port: in.Port, Driver: d, AMQP: in.AMQP}
}

func resolvedInputs(st *enginev1alpha1.ShadowTest) []enginev1alpha1.InputSpec {
	if isAMQPOnlyShadowTest(st) {
		out := make([]enginev1alpha1.InputSpec, len(st.Spec.Inputs))
		for i, in := range st.Spec.Inputs {
			out[i] = normalizeInputSpec(st, in)
		}
		return out
	}
	if len(st.Spec.Inputs) == 0 {
		return []enginev1alpha1.InputSpec{
			{Port: st.Spec.ServicePort, Driver: "http_request"},
		}
	}
	out := make([]enginev1alpha1.InputSpec, len(st.Spec.Inputs))
	for i, in := range st.Spec.Inputs {
		out[i] = normalizeInputSpec(st, in)
	}
	return out
}

func validateInputs(st *enginev1alpha1.ShadowTest) error {
	if len(st.Spec.Inputs) == 0 {
		return nil
	}

	rabbit := false
	nonRabbit := false
	for _, in := range st.Spec.Inputs {
		d := strings.TrimSpace(strings.ToLower(in.Driver))
		if d == "rabbitmq_message" {
			rabbit = true
		} else if d != "" || in.Port > 0 {
			nonRabbit = true
		}
	}
	if rabbit && nonRabbit {
		return fmt.Errorf("ShadowTest cannot mix rabbitmq_message inputs with HTTP/TCP inputs")
	}
	if isAMQPOnlyShadowTest(st) {
		for _, in := range st.Spec.Inputs {
			if in.AMQP == nil {
				return fmt.Errorf("rabbitmq_message input requires amqp block")
			}
			a := in.AMQP
			if strings.TrimSpace(a.ProdURL) == "" {
				return fmt.Errorf("amqp.prodUrl is required")
			}
			if strings.TrimSpace(a.Exchange) == "" {
				return fmt.Errorf("amqp.exchange is required")
			}
			if t := strings.TrimSpace(strings.ToLower(a.ExchangeType)); t != "" {
				switch t {
				case "topic", "direct", "fanout", "headers":
				default:
					return fmt.Errorf("amqp.exchangeType %q is not supported", a.ExchangeType)
				}
			}
			if strings.TrimSpace(a.RoutingKey) == "" {
				return fmt.Errorf("amqp.routingKey is required")
			}
			if strings.TrimSpace(a.TargetDependency) == "" {
				return fmt.Errorf("amqp.targetDependency is required")
			}
			if _, ok := dependencyByName(st, a.TargetDependency); !ok {
				return fmt.Errorf("amqp.targetDependency %q not found in spec.dependencies", a.TargetDependency)
			}
		}
		return nil
	}

	for _, in := range resolvedInputs(st) {
		switch in.Driver {
		case "http_request", "tcp_stream":
		default:
			return fmt.Errorf("unsupported Igris driver %q for port %d", in.Driver, in.Port)
		}
		if in.Port < 1 || in.Port > 65535 {
			return fmt.Errorf("input port %d out of range", in.Port)
		}
	}
	return nil
}

func inputPortName(driver string, port int32) string {
	name := sanitizeForDNS(fmt.Sprintf("%s-%d", strings.ReplaceAll(driver, "_", "-"), port))
	if len(name) > 15 {
		name = name[:15]
		name = strings.TrimRight(name, "-")
	}
	return name
}

func shadowServiceHost(shadowNS, serviceName string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, shadowNS)
}

func shadowServicePorts(st *enginev1alpha1.ShadowTest) []corev1.ServicePort {
	if isAMQPOnlyShadowTest(st) {
		return []corev1.ServicePort{{
			Name:       "ingress",
			Port:       st.Spec.ServicePort,
			TargetPort: intstr.FromString("ingress"),
			Protocol:   corev1.ProtocolTCP,
		}}
	}

	inputs := resolvedInputs(st)
	seen := map[int32]bool{}
	var ports []corev1.ServicePort

	if !seen[st.Spec.ServicePort] {
		ports = append(ports, corev1.ServicePort{
			Name:       "ingress",
			Port:       st.Spec.ServicePort,
			TargetPort: intstr.FromString("ingress"),
			Protocol:   corev1.ProtocolTCP,
		})
		seen[st.Spec.ServicePort] = true
	}

	for _, in := range inputs {
		if seen[in.Port] {
			continue
		}
		seen[in.Port] = true
		tp := intstr.FromInt32(in.Port)
		if in.Port == st.Spec.ServicePort {
			tp = intstr.FromString("ingress")
		}
		ports = append(ports, corev1.ServicePort{
			Name:       inputPortName(in.Driver, in.Port),
			Port:       in.Port,
			TargetPort: tp,
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return ports
}

func appContainerPortsFor(st *enginev1alpha1.ShadowTest) []corev1.ContainerPort {
	appPort := applicationPortFor(st)
	ports := []corev1.ContainerPort{{
		Name:          "http",
		ContainerPort: appPort,
		Protocol:      corev1.ProtocolTCP,
	}}
	if isAMQPOnlyShadowTest(st) {
		return ports
	}

	seen := map[int32]bool{appPort: true}
	for _, in := range resolvedInputs(st) {
		if in.Port == st.Spec.ServicePort {
			continue
		}
		if seen[in.Port] {
			continue
		}
		seen[in.Port] = true
		ports = append(ports, corev1.ContainerPort{
			Name:          inputPortName(in.Driver, in.Port),
			ContainerPort: in.Port,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	return ports
}

func igrisControlHosts(st *enginev1alpha1.ShadowTest, shadowNS string) (string, string, string) {
	return shadowServiceHost(shadowNS, shadowDeploymentName(st, roleControlA)),
		shadowServiceHost(shadowNS, shadowDeploymentName(st, roleControlB)),
		shadowServiceHost(shadowNS, shadowDeploymentName(st, roleCandidate))
}

func listenersSummary(st *enginev1alpha1.ShadowTest) string {
	if isAMQPOnlyShadowTest(st) {
		amqp, err := firstAMQPInput(st)
		if err != nil {
			return "rabbitmq_message"
		}
		return fmt.Sprintf("rabbitmq_message exchange=%s", amqp.Exchange)
	}
	var parts []string
	for _, in := range resolvedInputs(st) {
		parts = append(parts, fmt.Sprintf("%s:%d", in.Driver, in.Port))
	}
	return strings.Join(parts, ", ")
}
