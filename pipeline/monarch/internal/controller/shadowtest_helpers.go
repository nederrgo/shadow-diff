package controller

import (
	"fmt"
	"net"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/shadowspec"
)

func targetNamespaceFor(st *enginev1alpha1.ShadowTest) string {
	if st.Spec.TargetNamespace != "" {
		return st.Spec.TargetNamespace
	}
	return st.Namespace
}

func shadowNamespaceForCR(st *enginev1alpha1.ShadowTest) string {
	// Never truncate: names that project past 63 chars are rejected in reconcile
	// (sticky Failed). Silent truncation caused cross-ShadowTest namespace collisions.
	return sanitizeDNSLabel(fmt.Sprintf("shadow-%s-%s", st.Namespace, st.Name))
}

// validateShadowNamespaceName rejects namespace lifecycle outside shadow-*.
// Kubernetes RBAC cannot express name-prefix rules on Namespace objects; this
// guard is paired with ValidatingAdmissionPolicy/namespace-guard at deploy time.
func validateShadowNamespaceName(name string) error {
	if !strings.HasPrefix(name, "shadow-") {
		return fmt.Errorf("refusing namespace lifecycle outside shadow-* prefix: %q", name)
	}
	return nil
}

// sanitizeDNSLabel lowercases and replaces invalid DNS chars. It does not truncate.
func sanitizeDNSLabel(s string) string {
	s = strings.ToLower(s)
	s = invalidDNSChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "shadow"
	}
	return s
}

func sanitizeForDNS(s string) string {
	s = sanitizeDNSLabel(s)
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

func envoyContainerPorts(st *enginev1alpha1.ShadowTest) []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{Name: "ingress", ContainerPort: servicePortFor(st), Protocol: corev1.ProtocolTCP},
		{Name: "egress", ContainerPort: egressProxyPort, Protocol: corev1.ProtocolTCP},
	}
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

// primaryContainerPort returns the application port from the target Deployment.
// Priority: port named "http" → single declared port → default 8080.
// Returns an error when the deployment has multiple ports and none is named "http";
// the caller should ask the user to set spec.applicationPort explicitly.
func primaryContainerPort(target *appsv1.Deployment) (int32, error) {
	if len(target.Spec.Template.Spec.Containers) == 0 {
		return defaultApplicationPort, nil
	}
	ports := target.Spec.Template.Spec.Containers[0].Ports
	if len(ports) == 0 {
		return defaultApplicationPort, nil
	}
	for _, p := range ports {
		if strings.EqualFold(p.Name, "http") {
			return p.ContainerPort, nil
		}
	}
	if len(ports) == 1 {
		return ports[0].ContainerPort, nil
	}
	return 0, fmt.Errorf(
		"target has %d container ports with no port named 'http'; set spec.applicationPort to disambiguate",
		len(ports))
}

// safeServicePort returns a servicePort guaranteed not to equal applicationPort.
// Honours spec.ServicePort when it does not collide; otherwise computes an offset.
func safeServicePort(st *enginev1alpha1.ShadowTest) int32 {
	ap := st.Spec.ApplicationPort // always pre-resolved before this is called
	sp := st.Spec.ServicePort
	if sp == 0 {
		sp = defaultServicePort
	}
	if sp != ap {
		return sp
	}
	safe := ap + 8000
	if safe > 65535 {
		safe = ap - 8000
	}
	return safe
}

// resolveSpecDefaults fills in ApplicationPort and ServicePort from the target
// Deployment when the user omitted them. Mutates st.Spec in memory only — the CR
// stored in k8s is never patched. OldImage is persisted via ensureOldImage.
func resolveSpecDefaults(st *enginev1alpha1.ShadowTest, target *appsv1.Deployment) error {
	if st.Spec.ApplicationPort == 0 {
		port, err := primaryContainerPort(target)
		if err != nil {
			return err
		}
		st.Spec.ApplicationPort = port
	}

	st.Spec.ServicePort = safeServicePort(st)

	return nil
}

// Beru always runs as beru-local inside the shadow namespace, one per ShadowTest.
func beruGRPCAddressFor(_ *enginev1alpha1.ShadowTest, shadowNS string) string {
	return fmt.Sprintf("%s:%d", localBeruDNSHost(shadowNS), localBeruGRPCPort)
}

func beruHTTPHostFor(_ *enginev1alpha1.ShadowTest, shadowNS string) string {
	return fmt.Sprintf("%s:%d", localBeruDNSHost(shadowNS), localBeruHTTPPort)
}

// beruIngestURLFor is the base URL a shadow-pod sidecar posts egress reports to.
// It uses the ingest port rather than 8080 — see localBeruIngestPort.
func beruIngestURLFor(_ *enginev1alpha1.ShadowTest, shadowNS string) string {
	return fmt.Sprintf("http://%s:%d", localBeruDNSHost(shadowNS), localBeruIngestPort)
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

func resolveDependencyDefaults(dep enginev1alpha1.DependencySpec) (image string, port int32) {
	return shadowspec.ResolveDependencyDefaults(dep.Type, dep.Image, dep.Port)
}

// dependencyContainerEnv returns the environment a dependency image needs to
// start. Only Postgres has such a requirement: its entrypoint refuses to
// initialise without either a password or an explicit auth method, so a shadow
// dependency declared with no further configuration would CrashLoopBackOff and
// stall the readiness gate.
//
// Trust auth is safe here and nowhere else: the database is ephemeral, holds only
// replayed shadow traffic, and lives inside the shadow namespace.
func dependencyContainerEnv(dep enginev1alpha1.DependencySpec) []corev1.EnvVar {
	switch strings.ToLower(dep.Type) {
	case "postgres", "postgresql":
		return []corev1.EnvVar{
			{Name: "POSTGRES_HOST_AUTH_METHOD", Value: "trust"},
			{Name: "POSTGRES_USER", Value: "postgres"},
			{Name: "POSTGRES_DB", Value: "postgres"},
		}
	default:
		return nil
	}
}

func isMongoDependencyType(dep enginev1alpha1.DependencySpec) bool {
	switch strings.ToLower(dep.Type) {
	case "mongodb", "mongo":
		return true
	default:
		return false
	}
}

func isMongoDependency(dep enginev1alpha1.DependencySpec) bool {
	if isMongoDependencyType(dep) {
		return true
	}
	_, port := resolveDependencyDefaults(dep)
	return port == 27017
}
