/*
Copyright 2026 Shadow-Diff.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AMQPInputSpec configures native RabbitMQ shadow ingress (Phase 5b).
type AMQPInputSpec struct {
	// ProdURL is the production broker URL (e.g. amqp://prod-rabbitmq.default.svc:5672).
	ProdURL string `json:"prodUrl"`

	// Exchange is the production exchange to bind the shadow queue to.
	Exchange string `json:"exchange"`

	// ExchangeType is the AMQP exchange type (topic, direct, fanout, headers). Defaults to topic.
	// +kubebuilder:validation:Enum=topic;direct;fanout;headers
	// +optional
	ExchangeType string `json:"exchangeType,omitempty"`

	// RoutingKey is the binding routing key (e.g. "#" or "orders.*").
	RoutingKey string `json:"routingKey"`

	// TargetDependency is the name of a spec.dependencies entry (shadow brokers per role).
	TargetDependency string `json:"targetDependency"`
}

// InputSpec declares an ingress driver: HTTP listeners or RabbitMQ message capture.
type InputSpec struct {
	// Port is the TCP port Igris binds for HTTP inputs. Omit for rabbitmq_message.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`

	// Driver selects the ingress path (http_request or rabbitmq_message).
	// When empty on a port-based input, Monarch defaults to http_request.
	// +kubebuilder:validation:Enum=http_request;rabbitmq_message
	// +optional
	Driver string `json:"driver,omitempty"`

	// AMQP holds broker settings when driver is rabbitmq_message.
	// +optional
	AMQP *AMQPInputSpec `json:"amqp,omitempty"`

	// Addon is deprecated; use driver. Legacy value "http" maps to http_request.
	// +optional
	Addon string `json:"addon,omitempty"`
}

// DependencySpec declares an ephemeral backing service provisioned per shadow role.
type DependencySpec struct {
	// Name is the logical dependency id; used in resource names and DNS labels.
	Name string `json:"name"`

	// Type is the technology classification (e.g. rabbitmq, mongodb, redis).
	// Monarch uses this to auto-populate default images and ports.
	Type string `json:"type"`

	// Image is the container image (e.g. redis:7-alpine).
	// +optional
	Image string `json:"image,omitempty"`

	// Port is the TCP port exposed by the dependency container and Service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`

	// EnvVarInjection is the app container env var name set to the role-specific dependency
	// endpoint as host:port (e.g. redis-control-a.<shadow-ns>.svc.cluster.local:6379).
	EnvVarInjection string `json:"envVarInjection"`
}

// BeruSpec overrides the beru-local analytics backend workload.
type BeruSpec struct {
	// Image overrides the default Beru container image.
	// +optional
	Image string `json:"image,omitempty"`
}

// ShopSpec overrides the Shop mock-store workload.
type ShopSpec struct {
	// Image overrides the default Shop container image.
	// +optional
	Image string `json:"image,omitempty"`
}

// ShadowSoldierSpec overrides the shadow-soldier database egress capture sidecar.
// The sidecar is injected automatically into each shadow role that declares a
// proxied dependency (MongoDB, Redis, PostgreSQL, MSSQL).
type ShadowSoldierSpec struct {
	// Image overrides the default shadow-soldier container image.
	// +optional
	Image string `json:"image,omitempty"`
}

// EgressRelayRabbitMQSpec overrides the egress-relay-rabbitmq workload for AMQP-only ShadowTests.
type EgressRelayRabbitMQSpec struct {
	// Image overrides the default egress-relay-rabbitmq container image.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas defaults to 1.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources for the egress-relay-rabbitmq container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// IgrisRabbitMQSpec overrides the igris-rabbitmq workload for AMQP-only ShadowTests.
type IgrisRabbitMQSpec struct {
	// Image overrides the default igris-rabbitmq container image.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas defaults to 1.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources for the igris-rabbitmq container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// IgrisSpec overrides the always-deployed Igris workload.
type IgrisSpec struct {
	// Image overrides the default Igris container image.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas defaults to 1.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources for the Igris container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// StorageConfig configures Bring-Your-Own-Bucket (BYOB) S3-compatible object storage
// for asynchronous record/replay artifacts. Objects are keyed under
// shadow-diff/<namespace>/<test-name>/sessions/<session-id>/[ingress|egress]/.
// Monarch does not create buckets; callers provision storage out of band (e.g. AWS
// or the local MinIO fixture from testing/tools/e2e-reset-minikube.sh).
type StorageConfig struct {
	// Type selects the object-storage backend.
	// +kubebuilder:validation:Enum=s3
	// +kubebuilder:default=s3
	Type string `json:"type"`

	// BucketName is the S3 bucket that holds recorded traffic for this ShadowTest.
	BucketName string `json:"bucketName"`

	// Endpoint is an optional custom S3 API URL (MinIO / on-prem).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Region is the AWS region (also used as a MinIO region label).
	// +optional
	Region string `json:"region,omitempty"`

	// CredentialsSecretRef names a Secret in the ShadowTest CR namespace with
	// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY. Monarch copies it into the
	// shadow namespace (same name) before injecting secretKeyRef into pods.
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// RetentionPolicy controls whether Monarch deletes the ShadowTest's S3 prefix on CR deletion.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	// +optional
	RetentionPolicy string `json:"retentionPolicy,omitempty"`
}

// ShadowTestSpec defines the desired state of ShadowTest.
type ShadowTestSpec struct {
	// TargetDeployment is the name of the production Deployment whose pod template
	// (inline env vars from the first container) is mirrored for shadow pods.
	TargetDeployment string `json:"targetDeployment"`

	// TargetNamespace is the namespace containing TargetDeployment.
	// Defaults to the ShadowTest CR namespace when unset.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`

	// OldImage is the container image for Control-A and Control-B pods.
	// When unset, Monarch pins it from the target Deployment on first reconcile
	// and persists it on the CR. It is not overwritten on later reconciles.
	// +optional
	OldImage string `json:"oldImage,omitempty"`

	// NewImage is the container image for the Candidate pod.
	NewImage string `json:"newImage"`

	// ServicePort is the TCP port the Envoy ingress listener binds on in shadow pods.
	// Monarch always computes a conflict-free value relative to applicationPort — leave unset
	// unless you have a specific networking reason to pin it.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	ServicePort int32 `json:"servicePort,omitempty"`

	// ApplicationPort is the TCP port the app container listens on (Envoy forwards here).
	// When unset, Monarch derives it from the target Deployment's container ports (prefers the
	// port named "http"; falls back to the single declared port; fails if ambiguous).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	ApplicationPort int32 `json:"applicationPort,omitempty"`

	// BeruGRPCTimeout is the ext_proc gRPC timeout (e.g. "2s").
	// +optional
	BeruGRPCTimeout string `json:"beruGRPCTimeout,omitempty"`

	// Inputs defines Igris listener ports and drivers. When empty, Monarch defaults to
	// a single HTTP listener on servicePort.
	// +optional
	Inputs []InputSpec `json:"inputs,omitempty"`

	// Igris overrides image, replicas, or resources for the Igris traffic hub (HTTP/TCP ingress).
	// +optional
	Igris *IgrisSpec `json:"igris,omitempty"`

	// IgrisRabbitMQ overrides the igris-rabbitmq workload when inputs use rabbitmq_message.
	// +optional
	IgrisRabbitMQ *IgrisRabbitMQSpec `json:"igrisRabbitmq,omitempty"`

	// EgressRelayRabbitMQ overrides the egress-relay-rabbitmq Firehose translator when inputs use rabbitmq_message.
	// +optional
	EgressRelayRabbitMQ *EgressRelayRabbitMQSpec `json:"egressRelayRabbitmq,omitempty"`

	// SamplePercentage is the shared prod sampling gate (1-100, default 100) for
	// all input types. Uses github.com/shadow-diff/sample: V = FNV-1a-64(decoded
	// 16-byte W3C trace id) & 0xFF; keep iff (V*100)<(N*256). Empty/missing
	// traceparent is always dropped. Monarch seeds it by inputs[].driver: HTTP →
	// KaiselRule (ingress and egress); rabbitmq_message → igris-rabbitmq
	// (IGRIS_RMQ_SAMPLE_PERCENTAGE). RabbitMQ does not use Kaisel.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	SamplePercentage int `json:"samplePercentage,omitempty"`

	// MaxQPSPerPod caps requests/sec Igris will forward per shadow pod replica (default 50).
	// Monarch multiplies this by the shadow role replica count to compute IGRIS_MAX_CONCURRENCY,
	// the ingress load-shedding threshold that protects shadow pods from traffic spikes.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxQPSPerPod int `json:"maxQPSPerPod,omitempty"`

	// Beru overrides the beru-local analytics image (one per shadow namespace).
	// +optional
	Beru *BeruSpec `json:"beru,omitempty"`

	// Shop overrides the Shop mock-store image (always provisioned per shadow namespace).
	// +optional
	Shop *ShopSpec `json:"shop,omitempty"`

	// ShadowSoldier overrides the database egress capture sidecar image.
	// +optional
	ShadowSoldier *ShadowSoldierSpec `json:"shadowSoldier,omitempty"`

	// Dependencies lists ephemeral backing services (e.g. Redis) provisioned once per shadow role.
	// +optional
	Dependencies []DependencySpec `json:"dependencies,omitempty"`

	// Mode selects record (capture to S3) or replay (ABC + S3 readers). Defaults to record.
	// +kubebuilder:validation:Enum=record;replay
	// +kubebuilder:default=record
	// +optional
	Mode string `json:"mode,omitempty"`

	// SessionID pins the S3 session folder for replay (and optionally for record).
	// When unset in record mode, Monarch mints status.currentSessionID (and remints
	// on each replay→record transition so capture does not append into the prior folder).
	// +optional
	SessionID string `json:"sessionID,omitempty"`

	// Storage configures required BYOB S3-compatible object storage for this ShadowTest.
	// +required
	Storage *StorageConfig `json:"storage"`
}

// Operating modes for ShadowTestSpec.Mode.
const (
	ModeRecord = "record"
	ModeReplay = "replay"
)

// OperatingMode normalizes spec.mode, applying the same record default the
// +kubebuilder:default marker applies server-side. Objects that never round-trip
// through the API server (unit tests, fake clients) reach the same answer.
func (in *ShadowTest) OperatingMode() string {
	m := strings.TrimSpace(strings.ToLower(in.Spec.Mode))
	if m == "" {
		return ModeRecord
	}
	return m
}

// BootStep is the coarse position of a ShadowTest in the Monarch boot sequence.
// Consumed by Tusk to drive the live topology graph.
//
// Record and replay mode walk disjoint sub-paths: record never provisions the shadow
// roles, replay never activates the egress tap or binds AMQP. The constants below are
// the union of both sequences, so consumers must not assume every step occurs.
type BootStep string

const (
	// BootStepValidating covers spec validation and target Deployment resolution.
	BootStepValidating BootStep = "ValidatingInputs"
	// BootStepProvisioningSinks covers beru-local, Shop and Igris (and replay dependencies).
	BootStepProvisioningSinks BootStep = "ProvisioningSinks"
	// BootStepActivatingEgressTap covers KaiselRule eBPF capture (record mode only).
	BootStepActivatingEgressTap BootStep = "ActivatingEgressTap"
	// BootStepBindingAMQP covers the prod shadow queue bind (record mode only).
	BootStepBindingAMQP BootStep = "BindingAMQP"
	// BootStepProvisioningShadow covers the control-a/control-b/candidate roles (replay only).
	BootStepProvisioningShadow BootStep = "ProvisioningShadow"
	// BootStepReady is the terminal converged step.
	BootStepReady BootStep = "Ready"
	// BootStepFailed is the terminal sticky-failure step.
	BootStepFailed BootStep = "Failed"
)

// Condition types reported on ShadowTestStatus.Conditions.
const (
	ConditionReady       = "Ready"
	ConditionProgressing = "Progressing"
	ConditionDegraded    = "Degraded"
)

// Values of ShadowTestStatus.Phase.
const (
	PhaseProgressing = "Progressing"
	PhaseReady       = "Ready"
	PhaseFailed      = "Failed"
	// PhaseDeleting is set on the CR while reconcileDelete tears the stack down.
	PhaseDeleting = "Deleting"
	// PhaseDeleted is stream-only: published after finalizers are removed so
	// Tusk can drop the graph. It is never persisted — the CR is gone.
	PhaseDeleted = "Deleted"
)

// Values of ShadowTestStatus.KaiselPhase. Degraded means the capture rule failed
// to reconcile; Disabled means replay mode never opens the tap.
const (
	CapturePhaseReady    = "Ready"
	CapturePhaseDegraded = "Degraded"
	CapturePhaseDisabled = "Disabled"
)

// ComponentStatus reports per-component readiness for the topology graph.
type ComponentStatus struct {
	// IgrisReady is true when the ingress hub (igris or igris-rabbitmq) is Available.
	// +optional
	IgrisReady bool `json:"igrisReady"`

	// ShopReady is true when the per-ShadowTest egress mock store is Available.
	// +optional
	ShopReady bool `json:"shopReady"`

	// BeruReady is true when the beru-local analysis sink is Available.
	// +optional
	BeruReady bool `json:"beruReady"`

	// KaiselRuleActive is true when the eBPF capture rule reconciled cleanly (record mode).
	// +optional
	KaiselRuleActive bool `json:"kaiselRuleActive"`

	// AMQPBound is true once the prod shadow queue is bound, or when the ShadowTest
	// declares no AMQP ingress at all.
	// +optional
	AMQPBound bool `json:"amqpBound"`

	// IngressDrivers lists resolved spec.inputs[].driver values
	// (e.g. http_request, rabbitmq_message). Tusk uses this to gate topology nodes.
	// +optional
	IngressDrivers []string `json:"ingressDrivers,omitempty"`

	// ShadowRolesReady maps control-a/control-b/candidate to Deployment readiness.
	// Empty in record mode, where no shadow roles are provisioned.
	// +optional
	ShadowRolesReady map[string]bool `json:"shadowRolesReady,omitempty"`

	// TargetDeployment is the production Deployment this ShadowTest shadows.
	// +optional
	TargetDeployment string `json:"targetDeployment,omitempty"`
}

// ShadowTestStatus defines the observed state of ShadowTest.
type ShadowTestStatus struct {
	// Phase is a high-level summary of reconciliation (e.g. Ready, Progressing, Failed).
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message carries human-readable detail, including MVP limitations (skipped envFrom, etc.).
	// +optional
	Message string `json:"message,omitempty"`

	// ShadowNamespace is the dedicated namespace where control/candidate Deployments run.
	// +optional
	ShadowNamespace string `json:"shadowNamespace,omitempty"`

	// CaptureTargets lists discovered prod pod template labels (key=value, sorted).
	// +optional
	CaptureTargets []string `json:"captureTargets,omitempty"`

	// KaiselPhase summarizes Kaisel ingress capture reconciliation (Ready, Degraded).
	// +optional
	KaiselPhase string `json:"kaiselPhase,omitempty"`

	// IgrisEndpoint is the DNS host:port Monarch configured for capture forwarding.
	// +optional
	IgrisEndpoint string `json:"igrisEndpoint,omitempty"`

	// AmqpQueueName is the production broker queue Monarch declared (shadow-diff-<uid>).
	// +optional
	AmqpQueueName string `json:"amqpQueueName,omitempty"`

	// IgrisRabbitMQPhase summarizes igris-rabbitmq deployment readiness.
	// +optional
	IgrisRabbitMQPhase string `json:"igrisRabbitMQPhase,omitempty"`

	// CurrentSessionID is the active S3 session folder for this ShadowTest.
	// +optional
	CurrentSessionID string `json:"currentSessionID,omitempty"`

	// CurrentReplayExecutionID scopes Postgres diffs for one replay run of CurrentSessionID.
	// Minted when entering replay; injected as REPLAY_EXECUTION_ID on beru-local.
	// +optional
	CurrentReplayExecutionID string `json:"currentReplayExecutionID,omitempty"`

	// ReplayState tracks automated replay trigger progress ("" | started | completed).
	// +optional
	ReplayState string `json:"replayState,omitempty"`

	// BootStep is the current position in the Monarch boot sequence.
	// +optional
	BootStep BootStep `json:"bootStep,omitempty"`

	// Components reports per-component readiness for the topology graph.
	// +optional
	Components ComponentStatus `json:"components,omitzero"`

	// Conditions follow the standard Kubernetes condition contract.
	// Types: Ready, Progressing, Degraded.
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=st
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Boot Step",type=string,JSONPath=`.status.bootStep`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ShadowTest is the Schema for the shadowtests API.
type ShadowTest struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ShadowTest
	// +required
	Spec ShadowTestSpec `json:"spec"`

	// status defines the observed state of ShadowTest
	// +optional
	Status ShadowTestStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ShadowTestList contains a list of ShadowTest.
type ShadowTestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ShadowTest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ShadowTest{}, &ShadowTestList{})
}
