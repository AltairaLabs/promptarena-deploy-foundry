package main

import (
	"fmt"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

// resolveAgentName determines which agent/prompt this runtime serves.
// Priority: PROMPTPACK_AGENT env var > agents.entry > the pack's single prompt.
func resolveAgentName(cfg *runtimeConfig, pack *prompt.Pack) (string, error) {
	if cfg.AgentName != "" {
		return cfg.AgentName, nil
	}

	if pack.Agents != nil && pack.Agents.Entry != "" {
		return pack.Agents.Entry, nil
	}

	if len(pack.Prompts) == 1 {
		for name := range pack.Prompts {
			return name, nil
		}
	}

	return "", fmt.Errorf(
		"cannot determine agent name: set %s, define agents.entry in the pack, "+
			"or ensure the pack has exactly one prompt",
		envAgentName,
	)
}
