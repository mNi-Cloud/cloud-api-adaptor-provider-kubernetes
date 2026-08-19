package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sandboxv1alpha1 "github.com/mNi-Cloud/cloud-api-adaptor-provider-kubernetes/api/v1alpha1"
)

type createSandboxRequest struct {
	WorkloadRef sandboxv1alpha1.WorkloadReference `json:"workloadRef"`
	SandboxID   string                            `json:"sandboxID"`
	UserData    string                            `json:"userData"`
	VCPUs       int64                             `json:"vcpus,omitempty"`
	MemoryMiB   int64                             `json:"memoryMiB,omitempty"`
	Arch        string                            `json:"arch,omitempty"`
}

type sandbox struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

type sandboxProviderConfig struct {
	NetworkMTU int `json:"networkMTU"`
}

type sandboxClient interface {
	Config(ctx context.Context) (sandboxProviderConfig, error)
	Create(ctx context.Context, request createSandboxRequest) (sandbox, error)
	Delete(ctx context.Context, id string) error
}

func (c *httpSandboxClient) Config(ctx context.Context) (sandboxProviderConfig, error) {
	var result sandboxProviderConfig
	if err := c.do(ctx, http.MethodGet, "v1/config", nil, &result); err != nil {
		return sandboxProviderConfig{}, err
	}
	return result, nil
}

type httpSandboxClient struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

func newHTTPSandboxClient(endpoint, token string, client *http.Client) (*httpSandboxClient, error) {
	baseURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Pod Sandbox endpoint: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("pod sandbox endpoint must use http or https")
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("pod sandbox endpoint must include a host")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &httpSandboxClient{baseURL: baseURL, token: token, client: client}, nil
}

func (c *httpSandboxClient) Create(ctx context.Context, request createSandboxRequest) (sandbox, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return sandbox{}, fmt.Errorf("encode sandbox request: %w", err)
	}

	var result sandbox
	if err := c.do(ctx, http.MethodPost, "v1/sandboxes", bytes.NewReader(body), &result); err != nil {
		return sandbox{}, err
	}
	return result, nil
}

func (c *httpSandboxClient) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "v1/sandboxes/"+url.PathEscape(id), nil, nil)
}

func (c *httpSandboxClient) do(ctx context.Context, method, path string, body io.Reader, result any) error {
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimSuffix(c.baseURL.Path, "/") + "/" + path
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return fmt.Errorf("create Pod Sandbox request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call Pod Sandbox API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pod sandbox API returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if result == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode Pod Sandbox response: %w", err)
	}
	return nil
}
