// Package main implements the promptarena-deploy-omnia binary,
// an Omnia Kubernetes deploy adapter for PromptKit.
package main

import (
	"fmt"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"

	"github.com/AltairaLabs/promptarena-deploy-omnia/internal/omnia"
)

func main() {
	provider := omnia.NewProvider()
	if err := adaptersdk.Serve(provider); err != nil {
		fmt.Fprintf(os.Stderr, "omnia: %v\n", err)
		os.Exit(1)
	}
}
