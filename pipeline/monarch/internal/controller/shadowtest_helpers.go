package controller

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func targetNamespaceFor(st *enginev1alpha1.ShadowTest) string {
	if st.Spec.TargetNamespace != "" {
		return st.Spec.TargetNamespace
	}
	return st.Namespace
}

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
	if len(st.Spec.RecordAndReplay) == 0 {
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
		{Name: "ingress", ContainerPort: servicePortFor(st), Protocol: corev1.ProtocolTCP},
	}
	if len(st.Spec.RecordAndReplay) > 0 {
		ports = append(ports, corev1.ContainerPort{
			Name: "egress", ContainerPort: egressProxyPort, Protocol: corev1.ProtocolTCP,
		})
	}
	if hasMongoDependency(st) {
		ports = append(ports, corev1.ContainerPort{
			Name: "mongo-egress", ContainerPort: mongoProxyPort, Protocol: corev1.ProtocolTCP,
		})
	}
	return ports
}

const defaultServicePort int32 = 8888
const defaultApplicationPort int32 = 8080

func servicePortFor(st *enginev1alpha1.ShadowTest) int32 {
	if st.Spec.ServicePort > 0 {
		return st.Spec.ServicePort
	}
	return defaultServicePort
}

func applicationPortFor(st *enginev1alpha1.ShadowTest) int32 {
	if st.Spec.ApplicationPort > 0 {
		return st.Spec.ApplicationPort
	}
	if isAMQPOnlyShadowTest(st) {
		sp := servicePortFor(st)
		if sp < 65535 {
			return sp + 1
		}
		return sp - 1
	}
	return defaultApplicationPort
}

func beruGRPCAddressFor(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	if st.Spec.BeruGRPCAddress != "" {
		return st.Spec.BeruGRPCAddress
	}
	return fmt.Sprintf("%s:%d", localBeruDNSHost(shadowNS), localBeruGRPCPort)
}

func beruHTTPHostFor(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	if st.Spec.BeruGRPCAddress != "" {
		host, _, err := parseBeruHostPort(st.Spec.BeruGRPCAddress)
		if err != nil || host == "" {
			return defaultBeruHTTPAddress
		}
		return fmt.Sprintf("%s:8080", host)
	}
	return fmt.Sprintf("%s:%d", localBeruDNSHost(shadowNS), localBeruHTTPPort)
}

func beruOTLPEndpointFor(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	if st.Spec.BeruGRPCAddress != "" {
		return defaultBeruOTLPEndpoint
	}
	return fmt.Sprintf("http://%s:%d", localBeruDNSHost(shadowNS), localBeruOTLPPort)
}

func beruOTLPHTTPEndpointFor(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	if st.Spec.BeruGRPCAddress != "" {
		return defaultBeruOTLPHTTPEndpoint
	}
	return fmt.Sprintf("http://%s:%d", localBeruDNSHost(shadowNS), localBeruHTTPPort)
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

const defaultRecordAndReplayPort int32 = 80 // ponytail: HTTP egress default; override via host:port in spec

func parseRecordAndReplayTarget(rawHost string, defaultPort int32) (host string, port int32) {
	host = strings.TrimSpace(rawHost)
	port = defaultPort
	if host == "" {
		return "", defaultPort
	}
	if h, p, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
		if n, err := strconv.Atoi(p); err == nil {
			port = int32(n)
		}
	}
	return host, port
}

func recordAndReplayEntry(d enginev1alpha1.RecordAndReplayHostSpec) (host string, port int32, ignorePaths []string) {
	host, port = parseRecordAndReplayTarget(d.Host, defaultRecordAndReplayPort)
	return host, port, d.IgnoreRequestPaths
}

func recordAndReplayEgressDomains(st *enginev1alpha1.ShadowTest) []string {
	var hosts []string
	for _, d := range st.Spec.RecordAndReplay {
		host, port, _ := recordAndReplayEntry(d)
		if host == "" {
			continue
		}
		if port != defaultRecordAndReplayPort {
			hosts = append(hosts, fmt.Sprintf("%s:%d", host, port))
		} else {
			hosts = append(hosts, host)
		}
	}
	return egressVirtualHostDomains(hosts)
}

func resolveDependencyDefaults(dep enginev1alpha1.DependencySpec) (image string, port int32) {
	image = dep.Image
	port = dep.Port
	switch strings.ToLower(dep.Type) {
	case "rabbitmq":
		if image == "" {
			image = "rabbitmq:3-management-alpine"
		}
		if port == 0 {
			port = 5672
		}
	case "mongodb", "mongo":
		if image == "" {
			image = "mongo:6.0"
		}
		if port == 0 {
			port = 27017
		}
	case "redis":
		if image == "" {
			image = "redis:7-alpine"
		}
		if port == 0 {
			port = 6379
		}
	}
	return
}

func isMongoDependencyType(dep enginev1alpha1.DependencySpec) bool {
	switch strings.ToLower(dep.Type) {
	case "mongodb", "mongo":
		return true
	default:
		return false
	}
}
