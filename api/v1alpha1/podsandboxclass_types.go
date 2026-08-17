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

// PodSandboxClassSpec defines how PodVM runner Pods are placed and connected.
// It intentionally exposes Kubernetes metadata instead of depending on a
// particular CNI implementation.
type PodSandboxClassSpec struct {
	// RunnerImage contains the PodVM launcher.
	// +kubebuilder:validation:MinLength=1
	RunnerImage string `json:"runnerImage"`

	// PodVMImage is an OCI image that exports disk.qcow2 into /output.
	// +kubebuilder:validation:MinLength=1
	PodVMImage string `json:"podVMImage"`

	// NetworkMTU is configured inside the PodVM guest.
	// +kubebuilder:validation:Minimum=576
	// +kubebuilder:validation:Maximum=9216
	// +kubebuilder:default=1500
	NetworkMTU int32 `json:"networkMTU,omitempty"`

	// Annotations are copied to each runner Pod. CNI integrations can be
	// selected using their normal Pod annotations.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels are copied to each runner Pod. Controller-owned labels take
	// precedence when the same key is present.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// NodeSelector constrains runner Pods to compatible nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations allow runner Pods to use dedicated virtualization nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// KataDir is the host directory containing Cloud Hypervisor and the Kata
	// runtime assets used by the runner.
	// +kubebuilder:default="/opt/kata"
	// +optional
	KataDir string `json:"kataDir,omitempty"`

	// MemoryOverheadMiB is reserved in addition to guest memory.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=256
	// +optional
	MemoryOverheadMiB int64 `json:"memoryOverheadMiB,omitempty"`

	// StartupGraceSeconds delays readiness until guest initialization has
	// completed after Cloud Hypervisor starts.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=300
	// +kubebuilder:default=30
	// +optional
	StartupGraceSeconds int32 `json:"startupGraceSeconds,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=psbc

// PodSandboxClass is the Schema for the podsandboxclasses API
type PodSandboxClass struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of PodSandboxClass
	// +required
	Spec PodSandboxClassSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// PodSandboxClassList contains a list of PodSandboxClass
type PodSandboxClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodSandboxClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PodSandboxClass{}, &PodSandboxClassList{})
		return nil
	})
}
