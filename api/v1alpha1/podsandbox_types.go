/*
Copyright 2026.

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
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	ConditionReady       = "Ready"
	ConditionProgressing = "Progressing"
)

// PodSandboxSpec describes one Kata peer-pod VM.
type PodSandboxSpec struct {
	// SandboxID is the identity assigned by containerd/Kata.
	// +kubebuilder:validation:MinLength=1
	SandboxID string `json:"sandboxID"`

	// UserDataSecretRef contains the CAA-generated cloud-config in the
	// "userdata" key.
	UserDataSecretRef corev1.LocalObjectReference `json:"userDataSecretRef"`

	// ClassName selects the operator-defined runner and network integration.
	// +kubebuilder:validation:MinLength=1
	ClassName string `json:"className"`

	// +kubebuilder:validation:Minimum=1
	VCPUs int64 `json:"vcpus"`

	// +kubebuilder:validation:Minimum=128
	MemoryMiB int64 `json:"memoryMiB"`

	// +optional
	Arch string `json:"arch,omitempty"`
}

// PodSandboxStatus defines the observed state of PodSandbox.
type PodSandboxStatus struct {
	// +optional
	RunnerName string `json:"runnerName,omitempty"`
	// +optional
	IPs []string `json:"ips,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=psb
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ips[0]`

// PodSandbox is the Schema for the podsandboxes API
type PodSandbox struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of PodSandbox
	// +required
	Spec PodSandboxSpec `json:"spec"`

	// status defines the observed state of PodSandbox
	// +optional
	Status PodSandboxStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodSandboxList contains a list of PodSandbox
type PodSandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodSandbox `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PodSandbox{}, &PodSandboxList{})
		return nil
	})
}
