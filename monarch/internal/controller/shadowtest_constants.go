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

	egressProxyPort     int32  = 15001
	egressProxyURL      string = "http://127.0.0.1:15001"
	envHTTPProxy        string = "HTTP_PROXY"
	envHTTPSProxy       string = "HTTPS_PROXY"
	envNoProxy          string = "NO_PROXY"
	defaultNoProxyValue string = "127.0.0.1,localhost,.cluster.local,.svc"

	containerIgris               = "igris"
	defaultIgrisImage            = "igris:latest"
	configMapKeyListenersJSON    = "listeners.json"
	volumeNameIgrisConfig        = "igris-config"
	envControlAURL               = "CONTROL_A_URL"
	envControlBURL               = "CONTROL_B_URL"
	envCandidateURL              = "CANDIDATE_URL"
	envControlAAddr              = "CONTROL_A_ADDR"
	envControlBAddr              = "CONTROL_B_ADDR"
	envCandidateAddr             = "CANDIDATE_ADDR"
	envIgrisListenersFile        = "IGRIS_LISTENERS_FILE"
	defaultIgrisListenersPath    = "/etc/igris/listeners.json"
	igrisTerminationGraceSeconds = int64(35)
)

var envoyImagePullPolicy = corev1.PullIfNotPresent

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)
