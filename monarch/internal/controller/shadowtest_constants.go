package controller

import (
	"regexp"

	corev1 "k8s.io/api/core/v1"
)

const (
	finalizerName = "shadowtest.finalizers.shadow-diff.io"

	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelShadowTestName = "shadow-diff.io/shadowtest-name"
	labelShadowTestCRNS = "shadow-diff.io/shadowtest-cr-namespace"
	labelShadowTestUID  = "shadow-diff.io/shadowtest-uid"
	labelRole           = "shadow-diff.io/role"
	valueManagedBy      = "monarch"
	roleControlA        = "control-a"
	roleControlB        = "control-b"
	roleCandidate       = "candidate"

	containerEnvoySidecar = "envoy-sidecar"
	envShadowRole         = "SHADOW_ROLE"
	envoyImage            = "envoyproxy/envoy:v1.26-latest"
	configMapKeyEnvoyYAML = "envoy.yaml"
	volumeNameEnvoyConfig = "envoy-config"

	defaultBeruGRPCAddress = "beru.beru-system.svc.cluster.local:50051"
	defaultBeruGRPCTimeout = "2s"
	envBeruGRPCAddress     = "BERU_GRPC_ADDRESS"
)

var envoyImagePullPolicy = corev1.PullIfNotPresent

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)
