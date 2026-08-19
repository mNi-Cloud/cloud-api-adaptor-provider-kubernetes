package main

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	providers "github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers"
	"github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers/util/cloudinit"
)

type fakeCloudConfig struct {
	data string
	err  error
}

func TestGenerateUserDataCapsSandboxMTU(t *testing.T) {
	config := &cloudinit.CloudConfig{WriteFiles: []cloudinit.WriteFile{{
		Path:    agentProtocolForwarderConfigPath,
		Content: `{"pod-network":{"mtu":1500},"pod-name":"smoke","pod-namespace":"apps","pod-uid":"c848ed86-5137-4817-a2ee-d10a2bedf81a"}`,
	}}}

	userData, err := generateUserData(config, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(userData, `"mtu": 1400`) {
		t.Fatalf("sandbox MTU was not capped: %s", userData)
	}
	reference := workloadReference(config, "fallback")
	if reference.Namespace != "apps" || reference.Name != "smoke" || string(reference.UID) != "c848ed86-5137-4817-a2ee-d10a2bedf81a" {
		t.Fatalf("unexpected workload reference: %#v", reference)
	}
	if strings.Contains(config.WriteFiles[0].Content, `1400`) {
		t.Fatal("input cloud config was mutated")
	}
	for _, expected := range []string{
		agentProtocolForwarderConfigPath,
		apfOrderingDropInPath,
		cdhProxyDropInPath,
		"bootcmd:",
		"base64 -d",
		"systemctl, mask, --runtime, agent-protocol-forwarder.service",
		"systemctl, stop, --no-block, agent-protocol-forwarder.service",
		"systemctl, daemon-reload",
		"runcmd:",
		"systemctl, unmask, --runtime, agent-protocol-forwarder.service",
		"systemctl, restart, --no-block, agent-protocol-forwarder.service",
	} {
		if !strings.Contains(userData, expected) {
			t.Fatalf("generated cloud config does not contain %q: %s", expected, userData)
		}
	}
	if strings.Contains(userData, "restart, confidential-data-hub.service") {
		t.Fatalf("CDH must not be restarted after Kata Agent connects: %s", userData)
	}
	if !strings.Contains(userData, base64.StdEncoding.EncodeToString([]byte(apfOrderingDropIn))) {
		t.Fatalf("APF service ordering is missing: %s", userData)
	}
}

func (c fakeCloudConfig) Generate() (string, error) { return c.data, c.err }

type fakeSandboxClient struct {
	created createSandboxRequest
	result  sandbox
	err     error
	deleted string
	config  sandboxProviderConfig
}

func (c *fakeSandboxClient) Config(_ context.Context) (sandboxProviderConfig, error) {
	if c.err != nil {
		return sandboxProviderConfig{}, c.err
	}
	if c.config.NetworkMTU == 0 {
		return sandboxProviderConfig{NetworkMTU: 1500}, nil
	}
	return c.config, nil
}

func (c *fakeSandboxClient) Create(_ context.Context, request createSandboxRequest) (sandbox, error) {
	c.created = request
	return c.result, c.err
}

func (c *fakeSandboxClient) Delete(_ context.Context, id string) error {
	c.deleted = id
	return c.err
}

func TestCreateInstance(t *testing.T) {
	client := &fakeSandboxClient{config: sandboxProviderConfig{NetworkMTU: 1400}, result: sandbox{ID: "sandbox-1", Name: "pod-a", IPs: []string{"10.90.0.8"}}}
	provider, err := newKubernetesProvider(config{Endpoint: "https://sandbox.internal", Timeout: time.Second.String()}, client)
	if err != nil {
		t.Fatal(err)
	}

	instance, err := provider.CreateInstance(context.Background(), "pod-a", "sandbox-id", fakeCloudConfig{data: "#cloud-config"}, providers.InstanceTypeSpec{
		VCPUs: 2, Memory: 512, Arch: "amd64", Image: "podvm-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if instance.ID != "sandbox-1" || instance.IPs[0].String() != "10.90.0.8" {
		t.Fatalf("unexpected instance: %#v", instance)
	}
	if client.created.SandboxID != "sandbox-id" || client.created.UserData != "#cloud-config" || client.created.VCPUs != 2 {
		t.Fatalf("unexpected request: %#v", client.created)
	}
	if client.created.WorkloadRef.Name != "pod-a" {
		t.Fatalf("unexpected workload reference: %#v", client.created.WorkloadRef)
	}
}

func TestCreateInstanceReturnsPartialInstanceForCleanup(t *testing.T) {
	client := &fakeSandboxClient{result: sandbox{ID: "sandbox-1", Name: "pod-a", IPs: []string{"invalid"}}}
	provider, _ := newKubernetesProvider(config{Endpoint: "https://sandbox.internal"}, client)

	instance, err := provider.CreateInstance(context.Background(), "pod-a", "sandbox-id", fakeCloudConfig{}, providers.InstanceTypeSpec{})
	if err == nil || instance == nil || instance.ID != "sandbox-1" {
		t.Fatalf("expected partial instance and error, got instance=%#v err=%v", instance, err)
	}
}

func TestCreateInstancePropagatesAPIFailure(t *testing.T) {
	client := &fakeSandboxClient{err: errors.New("unavailable")}
	provider, _ := newKubernetesProvider(config{Endpoint: "https://sandbox.internal"}, client)

	if _, err := provider.CreateInstance(context.Background(), "pod-a", "sandbox-id", fakeCloudConfig{}, providers.InstanceTypeSpec{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteInstance(t *testing.T) {
	client := &fakeSandboxClient{}
	provider, _ := newKubernetesProvider(config{Endpoint: "https://sandbox.internal"}, client)
	if err := provider.DeleteInstance(context.Background(), "sandbox-1"); err != nil {
		t.Fatal(err)
	}
	if client.deleted != "sandbox-1" {
		t.Fatalf("unexpected deleted id: %q", client.deleted)
	}
}
