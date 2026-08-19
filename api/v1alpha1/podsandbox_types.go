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
	"k8s.io/apimachinery/pkg/types"
)

const (
	ConditionReady       = "Ready"
	ConditionProgressing = "Progressing"
)

// WorkloadReference identifies the Pod represented by a PodSandbox. The Pod
// may belong to another Kubernetes cluster, so this is an informational
// reference rather than an owner reference.
type WorkloadReference struct {
	// Namespace is the workload Pod namespace in its source cluster.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the workload Pod name in its source cluster.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// UID disambiguates recreated workload Pods when the source reports it.
	// +optional
	UID types.UID `json:"uid,omitempty"`
}

// PodSandboxSpec describes one Kata peer-pod VM.
// A PodSandbox represents one immutable workload execution. Replacing the
// workload requires creating a new PodSandbox.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type PodSandboxSpec struct {
	// WorkloadRef identifies the source Pod represented by this sandbox.
	WorkloadRef WorkloadReference `json:"workloadRef"`

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
	// ObservedGeneration is the generation reconciled into status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// RunnerRef identifies the management-cluster Pod hosting the PodVM.
	// +optional
	RunnerRef *corev1.LocalObjectReference `json:"runnerRef,omitempty"`

	// NodeName is the management-cluster Node hosting the runner.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// IPs contains addresses assigned to the PodSandbox.
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
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadRef.name`
// +kubebuilder:printcolumn:name="Workload Namespace",type=string,JSONPath=`.spec.workloadRef.namespace`,priority=1
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.className`
// +kubebuilder:printcolumn:name="vCPU",type=integer,JSONPath=`.spec.vcpus`,priority=1
// +kubebuilder:printcolumn:name="Memory MiB",type=integer,JSONPath=`.spec.memoryMiB`,priority=1
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.nodeName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ips[0]`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
