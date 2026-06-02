package controller

import (
	"fmt"
	"net"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func shadowNamespaceForCR(st *enginev1alpha1.ShadowTest) string {
	return sanitizeForDNS(fmt.Sprintf("shadow-%s-%s", st.Namespace, st.Name))
}

func sanitizeForDNS(s string) string {
	s = strings.ToLower(s)
	s = invalidDNSChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "shadow"
	}
	if len(s) > 63 {
		s = s[:63]
		s = strings.TrimRight(s, "-")
	}
	return s
}

func deploymentPodLabels(st *enginev1alpha1.ShadowTest, role string) map[string]string {
	return map[string]string{
		labelManagedBy:      valueManagedBy,
		labelShadowTestName: st.Name,
		labelShadowTestCRNS: st.Namespace,
		labelShadowTestUID:  string(st.UID),
		labelRole:           role,
	}
}

func envFromTarget(dep *appsv1.Deployment) ([]corev1.EnvVar, string) {
	var warn string
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return nil, "target Deployment has no containers; no env copied"
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) > 0 {
		warn = "target primary container uses envFrom; MVP copies only literal env vars (skipped envFrom)"
	}
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			if warn == "" {
				warn = "target primary container uses valueFrom; MVP copies only literal env vars"
			}
			break
		}
	}

	var out []corev1.EnvVar
	for _, e := range c.Env {
		if e.ValueFrom == nil {
			out = append(out, e)
		}
	}
	return out, warn
}

func appEnvWithEgressProxy(st *enginev1alpha1.ShadowTest, base []corev1.EnvVar) []corev1.EnvVar {
	if len(st.Spec.Downstreams) == 0 {
		return base
	}
	out := append([]corev1.EnvVar{}, base...)
	out = append(out,
		corev1.EnvVar{Name: envHTTPProxy, Value: egressProxyURL},
		corev1.EnvVar{Name: envHTTPSProxy, Value: egressProxyURL},
		corev1.EnvVar{Name: envNoProxy, Value: defaultNoProxyValue},
	)
	return out
}

func envoyContainerPorts(st *enginev1alpha1.ShadowTest) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{
		{Name: "ingress", ContainerPort: st.Spec.ServicePort, Protocol: corev1.ProtocolTCP},
	}
	if len(st.Spec.Downstreams) > 0 {
		ports = append(ports, corev1.ContainerPort{
			Name: "egress", ContainerPort: egressProxyPort, Protocol: corev1.ProtocolTCP,
		})
	}
	return ports
}

func applicationPortFor(st *enginev1alpha1.ShadowTest) int32 {
	if st.Spec.ApplicationPort > 0 {
		return st.Spec.ApplicationPort
	}
	if st.Spec.ServicePort < 65535 {
		return st.Spec.ServicePort + 1
	}
	return st.Spec.ServicePort - 1
}

func beruGRPCAddressFor(st *enginev1alpha1.ShadowTest) string {
	if st.Spec.BeruGRPCAddress != "" {
		return st.Spec.BeruGRPCAddress
	}
	return defaultBeruGRPCAddress
}

func beruGRPCTimeoutFor(st *enginev1alpha1.ShadowTest) string {
	if st.Spec.BeruGRPCTimeout != "" {
		return st.Spec.BeruGRPCTimeout
	}
	return defaultBeruGRPCTimeout
}

func parseBeruHostPort(address string) (host string, port int32, err error) {
	if !strings.Contains(address, ":") {
		return address, 50051, nil
	}
	h, p, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	var portNum int
	_, err = fmt.Sscanf(p, "%d", &portNum)
	if err != nil {
		return "", 0, err
	}
	return h, int32(portNum), nil
}
