package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sandboxv1alpha1 "github.com/mNi-Cloud/cloud-api-adaptor-provider-kubernetes/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
			Spec:       sandboxv1alpha1.PodSandboxSpec{SandboxID: sandboxID, ClassName: "cluster-a"},
		},
	).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", Token: "default-secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(createSandboxRequest{SandboxID: sandboxID, UserData: "userdata"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), authorizedClassContextKey{}, "cluster-b"))
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
		Spec:       sandboxv1alpha1.PodSandboxSpec{SandboxID: "one", ClassName: "cluster-a"},
	}).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", Token: "default-secret"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/psb-one", nil)
	request.SetPathValue("id", "psb-one")
	request = request.WithContext(context.WithValue(request.Context(), authorizedClassContextKey{}, "cluster-b"))
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
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "sandbox", Labels: map[string]string{AccessSecretLabel: "true"}}, Data: map[string][]byte{AccessSecretTokenKey: []byte("cluster-secret"), AccessSecretClassKey: []byte("cluster-a")}},
	).Build()
	server, err := New(client, Config{Namespace: "sandbox", ClassName: "default", TokenSecretName: "default-token"})
	if err != nil {
		t.Fatal(err)
	}
	className, err := server.authorizedClass(context.Background(), "cluster-secret")
	if err != nil {
		t.Fatal(err)
	}
	if className != "cluster-a" {
		t.Fatalf("authorized class = %q, want cluster-a", className)
	}
	className, err = server.authorizedClass(context.Background(), "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if className != "" {
		t.Fatalf("unknown token authorized class %q", className)
	}
}
