// Package main implements the promptarena-deploy-foundry binary, an Azure AI
// Foundry hosted-agent deploy adapter for PromptKit.
package main

import (
	"fmt"
	"os"

	"github.com/AltairaLabs/PromptKit/runtime/deploy/adaptersdk"

	"github.com/AltairaLabs/promptarena-deploy-foundry/internal/foundry"
)

func main() {
	provider := foundry.NewProvider()
	if err := adaptersdk.Serve(provider); err != nil {
		fmt.Fprintf(os.Stderr, "foundry: %v\n", err)
		os.Exit(1)
	}
}
