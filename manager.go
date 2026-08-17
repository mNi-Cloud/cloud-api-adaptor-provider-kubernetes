package main

import (
	"flag"

	providers "github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers"
)

var providerConfig config

type manager struct{}

func init() {
	providers.AddCloudProvider("kubernetes", &manager{})
}

func (*manager) ParseCmd(flags *flag.FlagSet) {
	registrar := providers.NewFlagRegistrar(flags)
	registrar.StringWithEnv(
		&providerConfig.Endpoint,
		"kubernetes-endpoint", "", "KUBERNETES_POD_SANDBOX_ENDPOINT",
		"Kubernetes Pod Sandbox API endpoint", providers.Required(),
	)
	registrar.StringWithEnv(
		&providerConfig.Token,
		"kubernetes-token", "", "KUBERNETES_POD_SANDBOX_TOKEN",
		"Kubernetes Pod Sandbox API bearer token", providers.Secret(),
	)

	registrar.StringWithEnv(
		&providerConfig.Timeout,
		"kubernetes-timeout", "2m", "KUBERNETES_POD_SANDBOX_TIMEOUT",
		"Kubernetes Pod Sandbox API request timeout",
	)
}

func (*manager) LoadEnv() {}

func (*manager) NewProvider() (providers.Provider, error) {
	return newKubernetesProvider(providerConfig, nil)
}
