package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSandboxClientLifecycle(t *testing.T) {
	var deleted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			var request createSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if request.SandboxID != "sandbox-id" {
				http.Error(w, "bad sandbox id", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandbox{ID: "sandbox-1", Name: request.Name, IPs: []string{"10.90.0.8"}})
		case r.Method == http.MethodDelete:
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newHTTPSandboxClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.Create(context.Background(), createSandboxRequest{Name: "pod-a", SandboxID: "sandbox-id"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "sandbox-1" {
		t.Fatalf("unexpected response: %#v", created)
	}
	if err := client.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if deleted != "/v1/sandboxes/sandbox-1" {
		t.Fatalf("unexpected delete path: %q", deleted)
	}
}
