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
