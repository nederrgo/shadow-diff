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

// KaiselRuleSpec defines the eBPF capture targets for a ShadowTest.
type KaiselRuleSpec struct {
	// TargetIPs are the live IPv4 addresses of Running target pods.
	// Monarch resolves these by listing pods with the target Deployment's labels.
	// +optional
	TargetIPs []string `json:"targetIPs,omitempty"`

	// TargetPorts restricts capture to these TCP ports (matched on source and
	// destination so both request and response directions are captured).
	// Empty means capture all TCP ports for the target IPs.
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	// +optional
	TargetPorts []uint16 `json:"targetPorts,omitempty"`
}

// KaiselRuleStatus reflects the observed state of the KaiselRule.
type KaiselRuleStatus struct {
	// ActiveNodes is how many kaisel DaemonSet pods have acknowledged this rule.
	// +optional
	ActiveNodes int32 `json:"activeNodes,omitempty"`

	// Phase is Active, Inactive, or Error.
	// +optional
	Phase string `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kr

// KaiselRule configures eBPF ingress capture for one ShadowTest.
type KaiselRule struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +optional
	Spec KaiselRuleSpec `json:"spec,omitempty"`

	// +optional
	Status KaiselRuleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KaiselRuleList contains a list of KaiselRule.
type KaiselRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KaiselRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KaiselRule{}, &KaiselRuleList{})
}
