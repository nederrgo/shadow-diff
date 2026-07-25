package controller

import (
	"regexp"

	corev1 "k8s.io/api/core/v1"
)

const (
	finalizerName = "shadowtest.finalizers.shadow-diff.io"

	labelManagedBy       = "app.kubernetes.io/managed-by"
	labelShadowTestName  = "shadow-diff.io/shadowtest-name"
	labelShadowTestCRNS  = "shadow-diff.io/shadowtest-cr-namespace"
	labelShadowTestUID   = "shadow-diff.io/shadowtest-uid"
	labelRole            = "shadow-diff.io/role"
	labelDependencyName  = "shadow-diff.io/dependency-name"
	labelResourceKind    = "shadow-diff.io/resource-kind"
	valueResourceKindDep = "dependency"
	valueManagedBy       = "monarch"
	roleControlA         = "control-a"
	roleControlB         = "control-b"
	roleCandidate        = "candidate"

	containerEnvoySidecar = "envoy-sidecar"
	containerApp          = "app"
	envShadowRole         = "SHADOW_ROLE"
	envShadowTestName     = "SHADOW_TEST_NAME"
	envoyImage            = "envoyproxy/envoy:v1.30-latest"
	configMapKeyEnvoyYAML = "envoy.yaml"
	volumeNameEnvoyConfig = "envoy-config"

	defaultBeruGRPCAddress      = "beru.beru-system.svc.cluster.local:50051"
	defaultBeruHTTPAddress      = "beru.beru-system.svc.cluster.local:8080"
	defaultBeruOTLPEndpoint     = "http://beru.beru-system.svc.cluster.local:4317"
	defaultBeruOTLPHTTPEndpoint = "http://beru.beru-system.svc.cluster.local:8080"
	defaultBeruIngestAddress    = "beru-ingest.shadow-system.svc.cluster.local:8080"
	defaultBeruGRPCTimeout      = "10s"
	beruSystemNamespace         = "beru-system"
	beruServiceName             = "beru"
	envBeruGRPCAddress          = "BERU_GRPC_ADDRESS"

	egressProxyPort int32 = 10001

	containerIptablesSetup = "iptables-setup"
	// debian:bookworm-slim ships both iptables (nft backend) and iptables-legacy so
	// the probe below works on any kernel: modern clusters (GKE COS, EKS Bottlerocket,
	// OpenShift 4.x) have nf_tables loaded; minikube kvm2 / older kubeadm nodes have
	// only x_tables (legacy) loaded.
	iptablesInitImage   = "debian:bookworm-slim"
	iptablesSetupScript = `apt-get update -qq && apt-get install -yqq --no-install-recommends iptables 2>/dev/null
if iptables -t nat -L >/dev/null 2>&1; then IPT=iptables; else IPT=iptables-legacy; fi
$IPT -t nat -A OUTPUT -p tcp -d 127.0.0.1/8 -j RETURN
$IPT -t nat -A OUTPUT -p tcp --dport 80 -j REDIRECT --to-port 10001
$IPT -t nat -A OUTPUT -p tcp --dport 8080 -j REDIRECT --to-port 10001`

	containerIgris               = "igris"
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

	containerRecorder       = "recorder"
	envRecorderListenAddr   = "RECORDER_LISTEN_ADDR"
	envRecorderOTLPGRPCAddr = "RECORDER_OTLP_GRPC_ADDR"
	envShopHTTPURL          = "SHOP_HTTP_URL"
	envRecorderSamplePct = "RECORDER_SAMPLE_PERCENTAGE"
	envBeruHTTPURL       = "BERU_HTTP_URL"
	recorderServicePort     = int32(8080)
	recorderOTLPPort        = int32(4317)

	shopName        = "shop"
	shopGRPCPort    = int32(50051)
	shopHTTPPort    = int32(8080)
	envShopGRPCAddr = "SHOP_GRPC_ADDR"
	envShopHTTPAddr = "SHOP_HTTP_ADDR"

	volumeNameLocalBeruData = "beru-sqlite-data"
)

var envoyImagePullPolicy = corev1.PullIfNotPresent

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)
