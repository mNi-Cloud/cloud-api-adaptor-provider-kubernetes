package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	providers "github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers"
	"github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers/util/cloudinit"
)

type kubernetesProvider struct {
	config config
	client sandboxClient
}

func newKubernetesProvider(cfg config, client sandboxClient) (*kubernetesProvider, error) {
	timeout := 2 * time.Minute
	if cfg.Timeout != "" {
		parsed, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("parse KUBERNETES_POD_SANDBOX_TIMEOUT: %w", err)
		}
		timeout = parsed
	}
	if client == nil && cfg.Endpoint != "" {
		httpClient := &http.Client{Timeout: timeout}
		created, err := newHTTPSandboxClient(cfg.Endpoint, cfg.Token, httpClient)
		if err != nil {
			return nil, err
		}
		client = created
	}
	return &kubernetesProvider{config: cfg, client: client}, nil
}

func (p *kubernetesProvider) CreateInstance(
	ctx context.Context,
	podName, sandboxID string,
	cloudConfig cloudinit.CloudConfigGenerator,
	spec providers.InstanceTypeSpec,
) (*providers.Instance, error) {
	if err := p.ConfigVerifier(); err != nil {
		return nil, err
	}
	providerConfig, err := p.client.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("get Kubernetes Pod Sandbox configuration: %w", err)
	}
	userData, err := generateUserData(cloudConfig, providerConfig.NetworkMTU)
	if err != nil {
		return nil, fmt.Errorf("generate Pod VM user data: %w", err)
	}

	created, err := p.client.Create(ctx, createSandboxRequest{
		Name:      podName,
		SandboxID: sandboxID,
		UserData:  userData,
		VCPUs:     spec.VCPUs,
		MemoryMiB: spec.Memory,
		Arch:      spec.Arch,
	})
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes Pod Sandbox: %w", err)
	}

	ips := make([]netip.Addr, 0, len(created.IPs))
	for _, rawIP := range created.IPs {
		ip, err := netip.ParseAddr(rawIP)
		if err != nil {
			return &providers.Instance{ID: created.ID, Name: created.Name},
				fmt.Errorf("parse Pod Sandbox IP %q: %w", rawIP, err)
		}
		ips = append(ips, ip)
	}
	if created.ID == "" || len(ips) == 0 {
		return &providers.Instance{ID: created.ID, Name: created.Name, IPs: ips},
			fmt.Errorf("pod sandbox API returned an incomplete instance")
	}

	return &providers.Instance{ID: created.ID, Name: created.Name, IPs: ips}, nil
}

const agentProtocolForwarderConfigPath = "/run/peerpod/apf.json"

const (
	guestProxyURL         = "http://192.0.2.1:3128"
	cdhProxyDropInPath    = "/etc/systemd/system/confidential-data-hub.service.d/proxy.conf"
	apfOrderingDropInPath = "/etc/systemd/system/agent-protocol-forwarder.service.d/ordering.conf"
	cdhProxyDropIn        = "[Service]\n" +
		"Environment=\"HTTP_PROXY=" + guestProxyURL + "\"\n" +
		"Environment=\"HTTPS_PROXY=" + guestProxyURL + "\"\n" +
		"Environment=\"NO_PROXY=127.0.0.1,localhost\"\n"
	apfOrderingDropIn = "[Unit]\n" +
		"Requires=netns@podns.service\n" +
		"After=cloud-init.service netns@podns.service\n"
)

func generateUserData(generator cloudinit.CloudConfigGenerator, networkMTU int) (string, error) {
	config, ok := generator.(*cloudinit.CloudConfig)
	if !ok || networkMTU <= 0 {
		return generator.Generate()
	}

	adjusted := *config
	adjusted.WriteFiles = append([]cloudinit.WriteFile(nil), config.WriteFiles...)
	var apfConfig []byte
	for i := range adjusted.WriteFiles {
		if adjusted.WriteFiles[i].Path != agentProtocolForwarderConfigPath {
			continue
		}
		var document map[string]any
		if err := json.Unmarshal([]byte(adjusted.WriteFiles[i].Content), &document); err != nil {
			return "", fmt.Errorf("parse agent protocol forwarder config: %w", err)
		}
		podNetwork, ok := document["pod-network"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("agent protocol forwarder config has no pod-network object")
		}
		current, ok := podNetwork["mtu"].(float64)
		if !ok {
			return "", fmt.Errorf("agent protocol forwarder config has no numeric MTU")
		}
		if int(current) > networkMTU {
			podNetwork["mtu"] = networkMTU
		}
		encoded, err := json.MarshalIndent(document, "", "    ")
		if err != nil {
			return "", fmt.Errorf("encode agent protocol forwarder config: %w", err)
		}
		adjusted.WriteFiles[i].Content = string(encoded)
		apfConfig = encoded
	}
	if len(apfConfig) == 0 {
		return "", fmt.Errorf("agent protocol forwarder config is missing")
	}
	generated, err := adjusted.Generate()
	if err != nil {
		return "", err
	}
	// bootcmd runs before the guest services are started. Installing and loading
	// the drop-in here avoids restarting CDH after Kata Agent has established its
	// TTRPC client, which would invalidate that connection.
	encodedAPFConfig := base64.StdEncoding.EncodeToString(apfConfig)
	encodedAPFOrdering := base64.StdEncoding.EncodeToString([]byte(apfOrderingDropIn))
	encodedDropIn := base64.StdEncoding.EncodeToString([]byte(cdhProxyDropIn))
	proxySetup := fmt.Sprintf(
		"\nbootcmd:\n"+
			"  - [systemctl, mask, --runtime, agent-protocol-forwarder.service]\n"+
			"  - [systemctl, stop, --no-block, agent-protocol-forwarder.service]\n"+
			"  - [mkdir, -p, /run/peerpod]\n"+
			"  - [sh, -c, \"echo %s | base64 -d > %s\"]\n"+
			"  - [mkdir, -p, /etc/systemd/system/agent-protocol-forwarder.service.d]\n"+
			"  - [sh, -c, \"echo %s | base64 -d > %s\"]\n"+
			"  - [mkdir, -p, /etc/systemd/system/confidential-data-hub.service.d]\n"+
			"  - [sh, -c, \"echo %s | base64 -d > %s\"]\n"+
			"  - [systemctl, daemon-reload]\n"+
			"\nruncmd:\n"+
			"  - [systemctl, unmask, --runtime, agent-protocol-forwarder.service]\n"+
			"  - [systemctl, daemon-reload]\n"+
			"  - [systemctl, restart, --no-block, agent-protocol-forwarder.service]\n",
		encodedAPFConfig,
		agentProtocolForwarderConfigPath,
		encodedAPFOrdering,
		apfOrderingDropInPath,
		encodedDropIn,
		cdhProxyDropInPath,
	)
	return generated + proxySetup, nil
}

func (p *kubernetesProvider) DeleteInstance(ctx context.Context, instanceID string) error {
	if instanceID == "" {
		return fmt.Errorf("DeleteInstance called with empty instanceID")
	}
	if err := p.ConfigVerifier(); err != nil {
		return err
	}
	if err := p.client.Delete(ctx, instanceID); err != nil {
		return fmt.Errorf("delete Kubernetes Pod Sandbox %q: %w", instanceID, err)
	}
	return nil
}

func (*kubernetesProvider) Teardown() error { return nil }

func (p *kubernetesProvider) ConfigVerifier() error {
	if p.config.Endpoint == "" {
		return fmt.Errorf("KUBERNETES_POD_SANDBOX_ENDPOINT is required")
	}
	if p.client == nil {
		return fmt.Errorf("pod sandbox API client is not configured")
	}
	return nil
}
