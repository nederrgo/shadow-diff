package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// soldierHealthPort / soldierHealthPath must match shadow-soldier/internal/health
// (GET /healthz on 0.0.0.0:19191). DB proxy routes stay on 127.0.0.1.
const (
	soldierHealthPort int32 = 19191
	soldierHealthPath       = "/healthz"
)

// soldierProtocols maps a dependency type to the wire protocol shadow-soldier
// parses for it. A dependency whose type is absent here is left connected
// directly to its Service — RabbitMQ in particular, whose egress is already
// diffed by egress-relay-rabbitmq via the broker Firehose.
var soldierProtocols = map[string]string{
	"mongodb":    "mongodb",
	"mongo":      "mongodb",
	"redis":      "redis",
	"postgres":   "postgresql",
	"postgresql": "postgresql",
	"mssql":      "mssql",
	"sqlserver":  "mssql",
}

// soldierRoute mirrors shadow-soldier's own route JSON.
type soldierRoute struct {
	Protocol string `json:"protocol"`
	Listen   int32  `json:"listen"`
	Upstream string `json:"upstream"`
}

func soldierProtocolFor(dep enginev1alpha1.DependencySpec) (string, bool) {
	proto, ok := soldierProtocols[strings.ToLower(strings.TrimSpace(dep.Type))]
	return proto, ok
}

// isProxiedDependency reports whether shadow-soldier sits in front of dep.
func isProxiedDependency(dep enginev1alpha1.DependencySpec) bool {
	_, ok := soldierProtocolFor(dep)
	return ok
}

// needsShadowSoldier reports whether this ShadowTest has anything to proxy.
func needsShadowSoldier(st *enginev1alpha1.ShadowTest) bool {
	for _, dep := range st.Spec.Dependencies {
		if isProxiedDependency(dep) {
			return true
		}
	}
	return false
}

// soldierRoutesFor builds the route table for one role. The listen port is the
// dependency's own port, so the only thing that changes in the application's
// connection string is the host.
func soldierRoutesFor(st *enginev1alpha1.ShadowTest, shadowNS, role string) []soldierRoute {
	var routes []soldierRoute
	for _, dep := range st.Spec.Dependencies {
		proto, ok := soldierProtocolFor(dep)
		if !ok {
			continue
		}
		_, port := resolveDependencyDefaults(dep)
		routes = append(routes, soldierRoute{
			Protocol: proto,
			Listen:   port,
			Upstream: dependencyEndpoint(shadowNS, dep.Name, role, port),
		})
	}
	return routes
}

// shadowSoldierContainer builds the sidecar for one shadow role, or nil when the
// ShadowTest declares no proxied dependency.
func shadowSoldierContainer(st *enginev1alpha1.ShadowTest, shadowNS, role string) (*corev1.Container, error) {
	routeList := soldierRoutesFor(st, shadowNS, role)
	if len(routeList) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(routeList)
	if err != nil {
		return nil, fmt.Errorf("render shadow-soldier routes: %w", err)
	}
	ports := make([]corev1.ContainerPort, 0, len(routeList)+1)
	ports = append(ports, corev1.ContainerPort{
		Name:          "health",
		ContainerPort: soldierHealthPort,
		Protocol:      corev1.ProtocolTCP,
	})
	for _, rt := range routeList {
		ports = append(ports, corev1.ContainerPort{
			Name:          sanitizeForDNS(rt.Protocol),
			ContainerPort: rt.Listen,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	return &corev1.Container{
		Name:            containerShadowSoldier,
		Image:           shadowSoldierImageFor(st),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports:           ports,
		Env: []corev1.EnvVar{
			{Name: envSoldierRoutes, Value: string(raw)},
			{Name: envShadowRole, Value: role},
			{Name: envShadowTestName, Value: st.Name},
			// Reports go to the ingest port, not 8080: the pod's iptables rules
			// REDIRECT outbound 8080 into Envoy's egress listener.
			{Name: envBeruHTTPURL, Value: beruIngestURLFor(st, shadowNS)},
			{
				// First use of the downward API in this operator. The pod name is
				// carried through to Beru for debugging only — role, not pod, is
				// what the diff correlates on.
				Name: envPodName,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			},
		},
		// HTTP /healthz on a non-loopback port (DB proxies stay on 127.0.0.1).
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: soldierHealthPath,
					Port: intstr.FromInt32(soldierHealthPort),
				},
			},
			InitialDelaySeconds: 2,
			PeriodSeconds:       5,
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("25m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	}, nil
}
