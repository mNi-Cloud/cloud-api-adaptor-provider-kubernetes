package server

import (
	"context"
	"testing"

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
