package main

import (
	"flag"
	"fmt"
	"os"

	providers "github.com/confidential-containers/cloud-api-adaptor/src/cloud-providers"
)

func main() {
	manager := providers.Get("kubernetes")
	if manager == nil {
		fmt.Fprintln(os.Stderr, "kubernetes provider was not registered")
		os.Exit(1)
	}
	flags := flag.NewFlagSet("plugin-smoke", flag.ContinueOnError)
	manager.ParseCmd(flags)
	if err := flags.Parse(nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	provider, err := manager.NewProvider()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := provider.ConfigVerifier(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("kubernetes provider plugin loaded")
}
