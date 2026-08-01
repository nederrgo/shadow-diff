package controller

import (
	"regexp"

	corev1 "k8s.io/api/core/v1"

	"github.com/shadow-diff/monarchpb"
)

const (
	finalizerName = "shadowtest.finalizers.shadow-diff.io"
	s3Finalizer   = "shadow-diff.io/s3-cleanup"

	labelManagedBy       = "app.kubernetes.io/managed-by"
	labelShadowTestName  = "shadow-diff.io/shadowtest-name"
	labelShadowTestCRNS  = "shadow-diff.io/shadowtest-cr-namespace"
	labelShadowTestUID   = "shadow-diff.io/shadowtest-uid"
	labelRole            = "shadow-diff.io/role"
	labelDependencyName  = "shadow-diff.io/dependency-name"
	labelResourceKind    = "shadow-diff.io/resource-kind"
	valueResourceKindDep = "dependency"
	valueManagedBy       = "monarch"
	// Role names double as the wire keys of ComponentStatus.ShadowRolesReady, so
	// they are defined from the shared contract rather than re-typed here.
	roleControlA  = monarchpb.RoleControlA
	roleControlB  = monarchpb.RoleControlB
	roleCandidate = monarchpb.RoleCandidate

	containerEnvoySidecar  = "envoy-sidecar"
	containerApp           = "app"
	containerShadowSoldier = "shadow-soldier"
	envShadowRole          = "SHADOW_ROLE"
	envShadowTestName      = "SHADOW_TEST_NAME"
	envSoldierRoutes       = "SOLDIER_ROUTES"
	envPodName             = "POD_NAME"
	envoyImage             = "envoyproxy/envoy:v1.30-latest"
	configMapKeyEnvoyYAML  = "envoy.yaml"
	volumeNameEnvoyConfig  = "envoy-config"

	defaultBeruGRPCTimeout = "10s"
	envBeruGRPCAddress     = "BERU_GRPC_ADDRESS"

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

	// shadowRoleReplicas is the replica count for each shadow role (control-a/b/candidate).
	// ponytail: hardcoded to 1 today; bump this (or replace with a CRD field) if per-role
	// scaling is ever needed — every consumer of the shadow pod count reads this constant.
	shadowRoleReplicas     int32 = 1
	defaultMaxQPSPerPod          = 50
	envIgrisMaxConcurrency       = "IGRIS_MAX_CONCURRENCY"

	envShopHTTPURL = "SHOP_HTTP_URL"
	envBeruHTTPURL = "BERU_HTTP_URL"

	shopName        = "shop"
	shopGRPCPort    = int32(50051)
	shopHTTPPort    = int32(8080)
	envShopGRPCAddr = "SHOP_GRPC_ADDR"
	envShopHTTPAddr = "SHOP_HTTP_ADDR"

	igrisAdminPort        = int32(9090)
	envIgrisAdminAddr     = "IGRIS_ADMIN_ADDR"
	defaultIgrisAdminAddr = ":9090"

	envOperatingMode = "OPERATING_MODE"
	envS3Bucket      = "S3_BUCKET"
	envS3Endpoint    = "S3_ENDPOINT"
	envS3Region      = "S3_REGION"
	envTestNamespace = "TEST_NAMESPACE"
	envTestName      = "TEST_NAME"
	envSessionID     = "SESSION_ID"
	envAWSAccessKey  = "AWS_ACCESS_KEY_ID"
	envAWSSecretKey  = "AWS_SECRET_ACCESS_KEY"
	secretKeyAccess  = "AWS_ACCESS_KEY_ID"
	secretKeySecret  = "AWS_SECRET_ACCESS_KEY"

	volumeNameLocalBeruData = "beru-wal-data"
)

var envoyImagePullPolicy = corev1.PullIfNotPresent

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)
