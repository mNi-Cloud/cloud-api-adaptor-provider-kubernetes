package controller

import (
	"context"
	"fmt"
	"maps"
	"time"

	sandboxv1alpha1 "github.com/mNi-Cloud/cloud-api-adaptor-provider-kubernetes/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	runnerContainerName     = "runner"
	runnerStateDirArg       = "--state-dir=/run/pod-sandbox"
	defaultRuntimeAssetsDir = "/opt/kata"
	defaultNetworkMTU       = 1500
	defaultOverheadMiB      = 256
	volumeImage             = "image"
	volumeConfig            = "config"
	volumeState             = "state"
	volumeRuntimeAssets     = "runtime-assets"
	volumeKVM               = "kvm"
	volumeTUN               = "tun"
)

type PodSandboxReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=sandbox.caa.mnicloud.jp,resources=podsandboxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sandbox.caa.mnicloud.jp,resources=podsandboxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sandbox.caa.mnicloud.jp,resources=podsandboxes/finalizers,verbs=update
// +kubebuilder:rbac:groups=sandbox.caa.mnicloud.jp,resources=podsandboxclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *PodSandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sandbox sandboxv1alpha1.PodSandbox
	if err := r.Get(ctx, req.NamespacedName, &sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !sandbox.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	var sandboxClass sandboxv1alpha1.PodSandboxClass
	if err := r.Get(ctx, types.NamespacedName{Name: sandbox.Spec.ClassName}, &sandboxClass); err != nil {
		return r.fail(ctx, &sandbox, "ClassUnavailable", err)
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: sandbox.Namespace, Name: sandbox.Spec.UserDataSecretRef.Name}, &secret); err != nil {
		return r.fail(ctx, &sandbox, "UserDataUnavailable", err)
	}
	if _, ok := secret.Data["userdata"]; !ok {
		return r.fail(ctx, &sandbox, "UserDataUnavailable", fmt.Errorf("secret %s has no userdata key", secret.Name))
	}
	desired := desiredRunner(&sandbox, &sandboxClass)
	if err := controllerutil.SetControllerReference(&sandbox, desired, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
		return r.fail(ctx, &sandbox, "RunnerCreateFailed", err)
	}
	var runner corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &runner); err != nil {
		if apierrors.IsNotFound(err) {
			return r.progressing(ctx, &sandbox, "WaitingForRunner", "The Pod Sandbox runner is being scheduled")
		}
		return ctrl.Result{}, err
	}
	if runner.Annotations["sandbox.caa.mnicloud.jp/template-generation"] != desired.Annotations["sandbox.caa.mnicloud.jp/template-generation"] {
		if err := r.Delete(ctx, &runner); err != nil && !apierrors.IsNotFound(err) {
			return r.fail(ctx, &sandbox, "RunnerDeleteFailed", err)
		}
		return r.progressing(ctx, &sandbox, "UpdatingRunner", "The Pod Sandbox runner is being updated")
	}
	if runner.Status.Phase == corev1.PodFailed || runner.Status.Phase == corev1.PodSucceeded {
		message := "The Pod Sandbox runner stopped before the PodVM became ready"
		if terminated := runnerTermination(&runner); terminated != nil && terminated.Message != "" {
			message = terminated.Message
		}
		return r.stopped(ctx, &sandbox, "RunnerFailed", message)
	}

	ready := podReady(&runner) && runner.Status.PodIP != ""
	base := sandbox.DeepCopy()
	sandbox.Status.ObservedGeneration = sandbox.Generation
	sandbox.Status.RunnerRef = &corev1.LocalObjectReference{Name: runner.Name}
	sandbox.Status.NodeName = runner.Spec.NodeName
	if runner.Status.PodIP == "" {
		sandbox.Status.IPs = nil
	} else {
		sandbox.Status.IPs = []string{runner.Status.PodIP}
	}
	apiMeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
		Type:               sandboxv1alpha1.ConditionReady,
		Status:             boolCondition(ready),
		ObservedGeneration: sandbox.Generation,
		Reason:             map[bool]string{true: "PodVMReady", false: "PodVMStarting"}[ready],
		Message:            map[bool]string{true: "The PodVM is ready", false: "Waiting for the PodVM runtime"}[ready],
	})
	if err := r.Status().Patch(ctx, &sandbox, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func desiredRunner(sandbox *sandboxv1alpha1.PodSandbox, sandboxClass *sandboxv1alpha1.PodSandboxClass) *corev1.Pod {
	networkMTU := sandboxClass.Spec.NetworkMTU
	if networkMTU == 0 {
		networkMTU = defaultNetworkMTU
	}
	runtimeAssetsDir := sandboxClass.Spec.RuntimeAssetsDir
	if runtimeAssetsDir == "" {
		runtimeAssetsDir = defaultRuntimeAssetsDir
	}
	overhead := sandboxClass.Spec.MemoryOverheadMiB
	if overhead == 0 {
		overhead = defaultOverheadMiB
	}
	overheadMiB := sandbox.Spec.MemoryMiB + overhead
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(sandbox.Spec.VCPUs, resource.DecimalSI),
			corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", overheadMiB)),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(sandbox.Spec.VCPUs, resource.DecimalSI),
			corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", overheadMiB)),
		},
	}
	labels := copyMap(sandboxClass.Spec.Labels)
	labels["app.kubernetes.io/name"] = "pod-sandbox-runner"
	labels["sandbox.caa.mnicloud.jp/sandbox"] = sandbox.Name
	annotations := copyMap(sandboxClass.Spec.Annotations)
	annotations["sandbox.caa.mnicloud.jp/class"] = sandboxClass.Name
	annotations["sandbox.caa.mnicloud.jp/template-generation"] = fmt.Sprintf("%d/%d", sandbox.Generation, sandboxClass.Generation)
	annotations["sandbox.caa.mnicloud.jp/workload-name"] = sandbox.Spec.WorkloadRef.Name
	if sandbox.Spec.WorkloadRef.Namespace != "" {
		annotations["sandbox.caa.mnicloud.jp/workload-namespace"] = sandbox.Spec.WorkloadRef.Namespace
	}
	if sandbox.Spec.WorkloadRef.UID != "" {
		annotations["sandbox.caa.mnicloud.jp/workload-uid"] = string(sandbox.Spec.WorkloadRef.UID)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sandbox.Name,
			Namespace:   sandbox.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			NodeSelector: copyMap(sandboxClass.Spec.NodeSelector),
			Tolerations:  append([]corev1.Toleration(nil), sandboxClass.Spec.Tolerations...),
			InitContainers: []corev1.Container{{
				Name:            "podvm-image",
				Image:           sandboxClass.Spec.PodVMImage,
				ImagePullPolicy: corev1.PullAlways,
				VolumeMounts: []corev1.VolumeMount{{
					Name:      volumeImage,
					MountPath: "/output",
				}},
			}},
			Containers: []corev1.Container{{
				Name:            runnerContainerName,
				Image:           sandboxClass.Spec.RunnerImage,
				ImagePullPolicy: corev1.PullAlways,
				Args: []string{
					"run",
					fmt.Sprintf("--cpus=%d", sandbox.Spec.VCPUs),
					fmt.Sprintf("--memory-mib=%d", sandbox.Spec.MemoryMiB),
					fmt.Sprintf("--network-mtu=%d", networkMTU),
					"--image-dir=/var/lib/podvm/image",
					"--config-dir=/var/lib/podvm/config",
					runnerStateDirArg,
					"--runtime-assets-dir=/opt/podvm-runtime",
					"--kernel=/var/lib/podvm/image/vmlinux",
					"--rootfs=/var/lib/podvm/image/rootfs.img",
				},
				Resources: resources,
				SecurityContext: &corev1.SecurityContext{
					Privileged:               ptr.To(true),
					AllowPrivilegeEscalation: ptr.To(true),
					RunAsUser:                ptr.To[int64](0),
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: volumeImage, MountPath: "/var/lib/podvm/image"},
					{Name: volumeConfig, MountPath: "/var/lib/podvm/config", ReadOnly: true},
					{Name: volumeState, MountPath: "/run/pod-sandbox"},
					{Name: volumeRuntimeAssets, MountPath: "/opt/podvm-runtime", ReadOnly: true},
					{Name: volumeKVM, MountPath: "/dev/kvm"},
					{Name: volumeTUN, MountPath: "/dev/net/tun"},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
						"/runner", "ready", runnerStateDirArg,
					}}},
					InitialDelaySeconds: 2,
					PeriodSeconds:       2,
					TimeoutSeconds:      1,
				},
			}},
			Volumes: []corev1.Volume{
				{Name: volumeImage, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: volumeConfig, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: sandbox.Spec.UserDataSecretRef.Name}}},
				{Name: volumeState, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: volumeRuntimeAssets, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: runtimeAssetsDir, Type: ptr.To(corev1.HostPathDirectory)}}},
				{Name: volumeKVM, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/kvm", Type: ptr.To(corev1.HostPathCharDev)}}},
				{Name: volumeTUN, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/net/tun", Type: ptr.To(corev1.HostPathCharDev)}}},
			},
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr.To[int64](30),
		},
	}
}

func copyMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	maps.Copy(result, source)
	return result
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func runnerTermination(pod *corev1.Pod) *corev1.ContainerStateTerminated {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == runnerContainerName {
			return pod.Status.ContainerStatuses[i].State.Terminated
		}
	}
	return nil
}

func (r *PodSandboxReconciler) progressing(ctx context.Context, sandbox *sandboxv1alpha1.PodSandbox, reason, message string) (ctrl.Result, error) {
	base := sandbox.DeepCopy()
	sandbox.Status.ObservedGeneration = sandbox.Generation
	apiMeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{Type: sandboxv1alpha1.ConditionReady, Status: metav1.ConditionFalse, ObservedGeneration: sandbox.Generation, Reason: reason, Message: message})
	if err := r.Status().Patch(ctx, sandbox, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *PodSandboxReconciler) fail(ctx context.Context, sandbox *sandboxv1alpha1.PodSandbox, reason string, reconcileErr error) (ctrl.Result, error) {
	base := sandbox.DeepCopy()
	sandbox.Status.ObservedGeneration = sandbox.Generation
	apiMeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{Type: sandboxv1alpha1.ConditionReady, Status: metav1.ConditionFalse, ObservedGeneration: sandbox.Generation, Reason: reason, Message: reconcileErr.Error()})
	if err := r.Status().Patch(ctx, sandbox, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, reconcileErr
}

func (r *PodSandboxReconciler) stopped(ctx context.Context, sandbox *sandboxv1alpha1.PodSandbox, reason, message string) (ctrl.Result, error) {
	base := sandbox.DeepCopy()
	sandbox.Status.ObservedGeneration = sandbox.Generation
	apiMeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{Type: sandboxv1alpha1.ConditionReady, Status: metav1.ConditionFalse, ObservedGeneration: sandbox.Generation, Reason: reason, Message: message})
	if err := r.Status().Patch(ctx, sandbox, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func boolCondition(value bool) metav1.ConditionStatus {
	if value {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func (r *PodSandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sandboxv1alpha1.PodSandbox{}).
		Owns(&corev1.Pod{}).
		Watches(&sandboxv1alpha1.PodSandboxClass{}, handler.EnqueueRequestsFromMapFunc(r.sandboxesForClass)).
		Named("pod-sandbox").
		Complete(r)
}

func (r *PodSandboxReconciler) sandboxesForClass(ctx context.Context, object client.Object) []reconcile.Request {
	var sandboxes sandboxv1alpha1.PodSandboxList
	if err := r.List(ctx, &sandboxes); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range sandboxes.Items {
		if sandboxes.Items[i].Spec.ClassName == object.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&sandboxes.Items[i])})
		}
	}
	return requests
}
