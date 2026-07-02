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

	defaultBeruGRPCAddress = "beru.beru-system.svc.cluster.local:50051"
	defaultBeruHTTPAddress = "beru.beru-system.svc.cluster.local:8080"
	defaultBeruOTLPEndpoint     = "http://beru.beru-system.svc.cluster.local:4317"
	defaultBeruOTLPHTTPEndpoint = "http://beru.beru-system.svc.cluster.local:8080"
	defaultBeruIngestAddress    = "beru-ingest.shadow-system.svc.cluster.local:8080"
	defaultBeruGRPCTimeout = "10s"
	beruSystemNamespace    = "beru-system"
	beruServiceName        = "beru"
	envBeruGRPCAddress     = "BERU_GRPC_ADDRESS"

	egressProxyPort int32  = 10001
	egressProxyURL  string = "http://127.0.0.1:10001"

	envHTTPProxy         string = "HTTP_PROXY"
	envHTTPSProxy        string = "HTTPS_PROXY"
	envNoProxy           string = "NO_PROXY"
	defaultNoProxyValue  string = "127.0.0.1,localhost,beru-ingest.shadow-system.svc.cluster.local,.cluster.local,.svc"

	beruIngestCluster = "beru_ingest"

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

	containerRecorder                  = "recorder"
	configMapKeyRecordAndReplayJSON    = "recordAndReplay.json"
	volumeNameRecorderConfig           = "recorder-config"
	envRecorderListenAddr              = "RECORDER_LISTEN_ADDR"
	envRecorderOTLPGRPCAddr            = "RECORDER_OTLP_GRPC_ADDR"
	envRecorderRecordAndReplayFile     = "RECORDER_RECORD_AND_REPLAY_FILE"
	envBeruHTTPURL                     = "BERU_HTTP_URL"
	defaultRecorderRecordAndReplayPath = "/etc/recorder/recordAndReplay.json"
	defaultBeruHTTPURL                 = "http://beru.beru-system.svc.cluster.local:8080"
	recorderServicePort                = int32(8080)
	recorderOTLPPort                   = int32(4317)

	volumeNameLocalBeruData = "beru-sqlite-data"
)

var envoyImagePullPolicy = corev1.PullIfNotPresent

var invalidDNSChars = regexp.MustCompile(`[^a-z0-9-]+`)
