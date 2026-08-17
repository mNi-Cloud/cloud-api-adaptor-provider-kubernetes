package controller

import (
	"testing"

	sandboxv1alpha1 "github.com/mNi-Cloud/cloud-api-adaptor-provider-kubernetes/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredRunnerUsesSharedKataNodeWithoutKataRuntimeClass(t *testing.T) {
	sandbox := &sandboxv1alpha1.PodSandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "sandbox-a", Namespace: "pod-sandbox-system"},
		Spec: sandboxv1alpha1.PodSandboxSpec{
			SandboxID:         "sandbox-id",
			UserDataSecretRef: corev1.LocalObjectReference{Name: "sandbox-a-userdata"},
			ClassName:         "juneau",
			VCPUs:             2,
			MemoryMiB:         512,
		},
	}
	sandboxClass := &sandboxv1alpha1.PodSandboxClass{
		ObjectMeta: metav1.ObjectMeta{Name: "juneau", Generation: 3},
		Spec: sandboxv1alpha1.PodSandboxClassSpec{
			RunnerImage:         "example.test/runner:v1",
			PodVMImage:          "example.test/podvm:v1",
			NetworkMTU:          1400,
			Annotations:         map[string]string{"juneau.loutres.me/subnet": "tenant-subnet"},
			NodeSelector:        map[string]string{"mnicloud.jp/kata": "true"},
			MemoryOverheadMiB:   256,
			StartupGraceSeconds: 45,
		},
	}

	pod := desiredRunner(sandbox, sandboxClass)
	if pod.Spec.RuntimeClassName != nil {
		t.Fatalf("runner must use runc; got RuntimeClass %q", *pod.Spec.RuntimeClassName)
	}
	if pod.Spec.NodeSelector["mnicloud.jp/kata"] != "true" {
		t.Fatalf("runner is not restricted to Kata-capable nodes: %#v", pod.Spec.NodeSelector)
	}
	if pod.Annotations["juneau.loutres.me/subnet"] != "tenant-subnet" {
		t.Fatalf("runner lost the requested Juneau subnet: %#v", pod.Annotations)
	}
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Image != "example.test/podvm:v1" {
		t.Fatalf("PodVM image is not exported by an init container: %#v", pod.Spec.InitContainers)
	}
	if pod.Spec.Volumes[0].EmptyDir == nil {
		t.Fatal("PodVM disk must use per-Runner ephemeral storage")
	}
	container := pod.Spec.Containers[0]
	if container.Image != "example.test/runner:v1" {
		t.Fatalf("unexpected runner image %q", container.Image)
	}
	if container.SecurityContext == nil || container.SecurityContext.Privileged == nil || !*container.SecurityContext.Privileged {
		t.Fatal("runner requires KVM/TUN and network namespace privileges")
	}
	if container.ReadinessProbe == nil || container.ReadinessProbe.Exec == nil {
		t.Fatal("runner readiness must use the guest-aware exec probe")
	}
	if got := container.ReadinessProbe.Exec.Command; len(got) != 4 || got[3] != "--startup-grace-seconds=45" {
		t.Fatalf("runner readiness lost the class startup grace: %#v", got)
	}
	wants := map[string]bool{"kata": false, "kvm": false, "tun": false, "image": false, "config": false, "state": false}
	for _, mount := range container.VolumeMounts {
		if _, ok := wants[mount.Name]; ok {
			wants[mount.Name] = true
		}
	}
	for name, found := range wants {
		if !found {
			t.Errorf("runner is missing %s mount", name)
		}
	}
}

func TestDesiredRunnerDoesNotRequireJuneauOrMNiMetadata(t *testing.T) {
	sandbox := &sandboxv1alpha1.PodSandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "sandbox-a", Namespace: "pod-sandbox-system", Generation: 1},
		Spec: sandboxv1alpha1.PodSandboxSpec{
			SandboxID:         "sandbox-id",
			UserDataSecretRef: corev1.LocalObjectReference{Name: "sandbox-a-userdata"},
			ClassName:         "generic",
			VCPUs:             1,
			MemoryMiB:         512,
		},
	}
	sandboxClass := &sandboxv1alpha1.PodSandboxClass{
		ObjectMeta: metav1.ObjectMeta{Name: "generic", Generation: 1},
		Spec: sandboxv1alpha1.PodSandboxClassSpec{
			RunnerImage: "example.test/runner:v1",
			PodVMImage:  "example.test/podvm:v1",
		},
	}

	pod := desiredRunner(sandbox, sandboxClass)
	if _, found := pod.Annotations["juneau.loutres.me/subnet"]; found {
		t.Fatal("generic runner unexpectedly contains a Juneau annotation")
	}
	if _, found := pod.Spec.NodeSelector["mnicloud.jp/kata"]; found {
		t.Fatal("generic runner unexpectedly contains an mNi node selector")
	}
	if pod.Spec.NodeSelector == nil {
		t.Fatal("node selector should be an empty writable map")
	}
}
