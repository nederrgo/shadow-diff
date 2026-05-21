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

// InputSpec declares an Igris listener port and input driver.
type InputSpec struct {
	// Port is the TCP port Igris binds for this input.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// Driver selects how Igris handles traffic on this port (http_request or tcp_stream).
	// When empty, Monarch infers from the port (servicePort and 80/443/8080 → http_request).
	// +kubebuilder:validation:Enum=http_request;tcp_stream
	// +optional
	Driver string `json:"driver,omitempty"`

	// Addon is deprecated; use driver. Legacy value "http" maps to http_request.
	// +optional
	Addon string `json:"addon,omitempty"`
}

// SiphonSpec configures the cluster-wide AF_PACKET capture agent.
type SiphonSpec struct {
	// Enabled disables Siphon config push when explicitly false.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Image overrides the Siphon DaemonSet container image.
	// +optional
	Image string `json:"image,omitempty"`

	// SampleRate is the percentage (0-100) of new TCP flows to sample.
	// +optional
	SampleRate *int32 `json:"sampleRate,omitempty"`
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
	TargetNamespace string `json:"targetNamespace"`

	// OldImage is the container image for Control-A and Control-B pods.
	OldImage string `json:"oldImage"`

	// NewImage is the container image for the Candidate pod.
	NewImage string `json:"newImage"`

	// ServicePort is the TCP port the Envoy ingress listener binds on in shadow pods.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ServicePort int32 `json:"servicePort"`

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

	// Igris overrides image, replicas, or resources for the Igris traffic hub (always deployed).
	// +optional
	Igris *IgrisSpec `json:"igris,omitempty"`

	// Siphon configures kernel-level traffic capture to Igris.
	// +optional
	Siphon *SiphonSpec `json:"siphon,omitempty"`
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

	// CaptureTargets lists production pod IPs pushed to Siphon agents.
	// +optional
	CaptureTargets []string `json:"captureTargets,omitempty"`

	// SiphonPhase summarizes Siphon config push (Ready, Degraded, Disabled).
	// +optional
	SiphonPhase string `json:"siphonPhase,omitempty"`

	// IgrisEndpoint is the DNS host:port Monarch configured for capture forwarding.
	// +optional
	IgrisEndpoint string `json:"igrisEndpoint,omitempty"`
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
