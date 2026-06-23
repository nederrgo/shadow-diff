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

// InputSpec declares an ingress driver: HTTP/TCP listeners or RabbitMQ message capture.
type InputSpec struct {
	// Port is the TCP port Igris binds for HTTP/TCP inputs. Omit for rabbitmq_message.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`

	// Driver selects the ingress path (http_request, tcp_stream, or rabbitmq_message).
	// When empty on a port-based input, Monarch infers from the port.
	// +kubebuilder:validation:Enum=http_request;tcp_stream;rabbitmq_message
	// +optional
	Driver string `json:"driver,omitempty"`

	// AMQP holds broker settings when driver is rabbitmq_message.
	// +optional
	AMQP *AMQPInputSpec `json:"amqp,omitempty"`

	// Addon is deprecated; use driver. Legacy value "http" maps to http_request.
	// +optional
	Addon string `json:"addon,omitempty"`
}

// SiphonSpec configures Pixie eBPF streaming capture for L1 ingress.
type SiphonSpec struct {
	// Enabled disables Pixie stream rules when explicitly false.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MaxPayloadSize is the max bytes to parse per HTTP/2 frame (default 65536).
	// +optional
	MaxPayloadSize int64 `json:"maxPayloadSize,omitempty"`

	// ExcludePaths are regex strings to drop healthchecks/traffic at the kernel layer.
	// +optional
	ExcludePaths []string `json:"excludePaths,omitempty"`
}

// RecordAndReplayHostSpec declares an outbound host for egress record/replay.
type RecordAndReplayHostSpec struct {
	// Host is the record-and-replay hostname (matches :authority / Host on proxied requests).
	Host string `json:"host"`

	// IgnoreRequestPaths are JSONPath expressions stripped before egress hashing (e.g. "$.timestamp").
	// +optional
	IgnoreRequestPaths []string `json:"ignoreRequestPaths,omitempty"`
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

// RecorderSpec overrides the Recorder egress parser workload.
type RecorderSpec struct {
	// Image overrides the default Recorder container image.
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
	OldImage string `json:"oldImage"`

	// NewImage is the container image for the Candidate pod.
	NewImage string `json:"newImage"`

	// ServicePort is the TCP port the Envoy ingress listener binds on in shadow pods.
	// Defaults to 8888 when unset.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	ServicePort int32 `json:"servicePort,omitempty"`

	// ApplicationPort is the TCP port the app container listens on (Envoy forwards here).
	// Must differ from ServicePort when Envoy fronts ingress. Defaults to servicePort+1 if unset.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	ApplicationPort int32 `json:"applicationPort,omitempty"`

	// BeruGRPCAddress is the host:port of the Beru ext_proc gRPC service.
	// +optional
	BeruGRPCAddress string `json:"beruGRPCAddress,omitempty"`

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

	// Siphon configures kernel-level traffic capture to Igris.
	// +optional
	Siphon *SiphonSpec `json:"siphon,omitempty"`

	// Recorder overrides the Recorder image when spec.recordAndReplay enables egress recording.
	// +optional
	Recorder *RecorderSpec `json:"recorder,omitempty"`

	// RecordAndReplay lists outbound hosts trapped by the egress proxy for strict replay.
	// +optional
	RecordAndReplay []RecordAndReplayHostSpec `json:"recordAndReplay,omitempty"`

	// Dependencies lists ephemeral backing services (e.g. Redis) provisioned once per shadow role.
	// +optional
	Dependencies []DependencySpec `json:"dependencies,omitempty"`

	// Language declares the application's runtime language (e.g. nodejs, python, java).
	// Monarch uses this to automatically inject the correct OpenTelemetry Operator agents.
	// +kubebuilder:validation:Enum=java;python;nodejs;dotnet;go
	// +optional
	Language string `json:"language,omitempty"`
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

	// SiphonPhase summarizes Pixie stream rule reconciliation (Ready, Degraded, Disabled).
	// +optional
	SiphonPhase string `json:"siphonPhase,omitempty"`

	// IgrisEndpoint is the DNS host:port Monarch configured for capture forwarding.
	// +optional
	IgrisEndpoint string `json:"igrisEndpoint,omitempty"`

	// AmqpQueueName is the production broker queue Monarch declared (shadow-diff-<uid>).
	// +optional
	AmqpQueueName string `json:"amqpQueueName,omitempty"`

	// IgrisRabbitMQPhase summarizes igris-rabbitmq deployment readiness.
	// +optional
	IgrisRabbitMQPhase string `json:"igrisRabbitMQPhase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=st

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
