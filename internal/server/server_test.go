package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sandboxv1alpha1 "github.com/mNi-Cloud/cloud-api-adaptor-provider-kubernetes/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateSandboxPersistsWorkloadReference(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sandboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&sandboxv1alpha1.PodSandbox{}).
		WithObjects(&sandboxv1alpha1.PodSandboxClass{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}}).
		Build()
	server, err := New(kubeClient, Config{Namespace: "provider-system", ClassName: "cluster-a", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	workloadRef := sandboxv1alpha1.WorkloadReference{
		Namespace: "applications",
		Name:      "api-7d9c",
		UID:       "c848ed86-5137-4817-a2ee-d10a2bedf81a",
	}
	sandboxID := "sandbox-id"
	key := types.NamespacedName{Namespace: "sandbox", Name: sandboxName(sandboxID)}
	updated := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var sandbox sandboxv1alpha1.PodSandbox
			if err := kubeClient.Get(context.Background(), key, &sandbox); err == nil {
				sandbox.Status.IPs = []string{"10.90.0.8"}
				sandbox.Status.Conditions = []metav1.Condition{{
					Type:               sandboxv1alpha1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             "PodVMReady",
					Message:            "The PodVM is ready",
					LastTransitionTime: metav1.Now(),
				}}
				updated <- kubeClient.Status().Update(context.Background(), &sandbox)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		updated <- context.DeadlineExceeded
	}()

	body, err := json.Marshal(createSandboxRequest{
		WorkloadRef: workloadRef,
		SandboxID:   sandboxID,
		UserData:    "userdata",
		VCPUs:       2,
		MemoryMiB:   512,
		Arch:        "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewReader(body)).WithContext(requestContext)
	request = request.WithContext(context.WithValue(request.Context(), authorizedAccessContextKey{}, authorizedAccess{ClassName: "cluster-a", Namespace: "sandbox"}))
	response := httptest.NewRecorder()
	server.createSandbox(response, request)
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var sandbox sandboxv1alpha1.PodSandbox
	if err := kubeClient.Get(context.Background(), key, &sandbox); err != nil {
		t.Fatal(err)
	}
	if sandbox.Spec.WorkloadRef != workloadRef {
		t.Fatalf("workload reference = %#v, want %#v", sandbox.Spec.WorkloadRef, workloadRef)
	}
}

func TestEnsureTokenSecret(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ensureTokenSecret(context.Background()); err != nil {
		t.Fatal(err)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: "sandbox", Name: "pod-sandbox-api"}
	if err := client.Get(context.Background(), key, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data["token"]) != 64 {
		t.Fatalf("generated token has %d bytes, want 64 hex characters", len(secret.Data["token"]))
	}
	first := string(secret.Data["token"])
	if err := server.ensureTokenSecret(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), key, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["token"]) != first {
		t.Fatal("existing token was rotated")
	}
}

func TestCreateSandboxRejectsAnotherClassIdentity(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sandboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	sandboxID := "shared-sandbox-id"
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&sandboxv1alpha1.PodSandboxClass{ObjectMeta: metav1.ObjectMeta{Name: "cluster-b"}},
		&sandboxv1alpha1.PodSandbox{
			ObjectMeta: metav1.ObjectMeta{Name: sandboxName(sandboxID), Namespace: "sandbox"},
			Spec: sandboxv1alpha1.PodSandboxSpec{
				WorkloadRef: sandboxv1alpha1.WorkloadReference{Namespace: "apps", Name: "workload-a"},
				SandboxID:   sandboxID,
				ClassName:   "cluster-a",
			},
		},
	).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", Token: "default-secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(createSandboxRequest{
		WorkloadRef: sandboxv1alpha1.WorkloadReference{Namespace: "apps", Name: "workload-a"},
		SandboxID:   sandboxID,
		UserData:    "userdata",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authorizedAccessContextKey{}, authorizedAccess{ClassName: "cluster-b", Namespace: "sandbox"}))
	response := httptest.NewRecorder()
	server.createSandbox(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
}

func TestDeleteSandboxRejectsAnotherClass(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sandboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&sandboxv1alpha1.PodSandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "psb-one", Namespace: "sandbox"},
		Spec: sandboxv1alpha1.PodSandboxSpec{
			WorkloadRef: sandboxv1alpha1.WorkloadReference{Name: "workload-a"},
			SandboxID:   "one",
			ClassName:   "cluster-a",
		},
	}).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", Token: "default-secret"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/psb-one", nil)
	request.SetPathValue("id", "psb-one")
	request = request.WithContext(context.WithValue(request.Context(), authorizedAccessContextKey{}, authorizedAccess{ClassName: "cluster-b", Namespace: "sandbox"}))
	response := httptest.NewRecorder()
	server.deleteSandbox(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusForbidden, response.Body.String())
	}
}

func TestEnsureTokenSecretRejectsEmptyToken(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-token", Namespace: "sandbox"},
	}).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", TokenSecretName: "custom-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ensureTokenSecret(context.Background()); err == nil {
		t.Fatal("empty token secret was accepted")
	}
}

func TestAuthorizedClassUsesAccessSecret(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "default-token", Namespace: "sandbox"}, Data: map[string][]byte{"token": []byte("default-secret")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "sandbox", Labels: map[string]string{AccessSecretLabel: "true"}}, Data: map[string][]byte{AccessSecretTokenKey: []byte("cluster-secret"), AccessSecretClassKey: []byte("cluster-a"), AccessSecretNamespaceKey: []byte("cluster-a-sandboxes")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "sandbox", Labels: map[string]string{AccessSecretLabel: "true"}}, Data: map[string][]byte{AccessSecretTokenKey: []byte("invalid-secret"), AccessSecretClassKey: []byte("cluster-b")}},
	).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", TokenSecretName: "default-token"})
	if err != nil {
		t.Fatal(err)
	}
	access, err := server.authorizedAccess(context.Background(), "cluster-secret")
	if err != nil {
		t.Fatal(err)
	}
	if access != (authorizedAccess{ClassName: "cluster-a", Namespace: "cluster-a-sandboxes"}) {
		t.Fatalf("authorized access = %#v", access)
	}
	access, err = server.authorizedAccess(context.Background(), "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if access != (authorizedAccess{}) {
		t.Fatalf("unknown token authorized access %#v", access)
	}
	if _, err := server.authorizedAccess(context.Background(), "invalid-secret"); err == nil {
		t.Fatal("access secret without namespace was accepted")
	}
}
