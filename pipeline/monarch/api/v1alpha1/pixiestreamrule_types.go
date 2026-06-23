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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PixieStreamRuleSpec defines Pixie eBPF capture targeting for a ShadowTest.
type PixieStreamRuleSpec struct {
	// ShadowTestRef is the owning ShadowTest as "<namespace>/<name>".
	ShadowTestRef string `json:"shadowTestRef"`

	// Active when false signals Pixie to detach capture immediately.
	Active bool `json:"active"`

	// TargetNamespace is the production namespace containing captured pods.
	TargetNamespace string `json:"targetNamespace"`

	// TargetLabels are prod pod template labels from the target Deployment.
	// +optional
	TargetLabels map[string]string `json:"targetLabels,omitempty"`

	// TargetPorts are ingress listener ports from ShadowTest inputs.
	// +optional
	TargetPorts []int32 `json:"targetPorts,omitempty"`

	// OTelEndpoint is the gRPC OTLP export destination for ingress (server-side) spans.
	// +optional
	OTelEndpoint string `json:"otelEndpoint,omitempty"`

	// RecorderOTelEndpoint is gRPC OTLP export for egress spans (client or in-cluster server-side).
	// +optional
	RecorderOTelEndpoint string `json:"recorderOtelEndpoint,omitempty"`

	// RecordAndReplayHosts are downstream hostnames for egress PxL Host-header filtering.
	// +optional
	RecordAndReplayHosts []string `json:"recordAndReplayHosts,omitempty"`

	// MaxPayloadSize is the max bytes to parse per HTTP/2 frame.
	// +optional
	MaxPayloadSize int64 `json:"maxPayloadSize,omitempty"`

	// ExcludePaths are regex strings dropped at the kernel layer.
	// +optional
	ExcludePaths []string `json:"excludePaths,omitempty"`
}

// PixieStreamRuleStatus defines the observed state of PixieStreamRule.
type PixieStreamRuleStatus struct {
	// Phase is Active, Inactive, or Error.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message carries human-readable detail.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=psr

// PixieStreamRule configures Pixie out-of-band OTel streaming for one ShadowTest.
type PixieStreamRule struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec PixieStreamRuleSpec `json:"spec"`

	// +optional
	Status PixieStreamRuleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PixieStreamRuleList contains a list of PixieStreamRule.
type PixieStreamRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PixieStreamRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PixieStreamRule{}, &PixieStreamRuleList{})
}
