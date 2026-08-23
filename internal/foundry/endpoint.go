package foundry

import (
	"fmt"
	"net/url"

	"github.com/AltairaLabs/promptarena/deploy"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
)

// invocationsURL builds the address a caller POSTs a turn to.
//
// This is the one thing a user needs after a successful deploy, and until now
// nothing surfaced it: not the apply output, not status, not the config they
// wrote. It lived only in scripts/invoke.sh, so the path to a first request was
// to read a shell script in the repo and reassemble the URL by hand.
//
// The adapter can build this safely because it owns the route: it is the same
// path the client calls, derived from config the adapter already validated,
// not a guess at someone else's console layout.
func invocationsURL(cfg *Config, agentName string) string {
	if cfg == nil || cfg.Account == "" || cfg.Project == "" || agentName == "" {
		return ""
	}
	return fmt.Sprintf(
		"https://%s.services.ai.azure.com/api/projects/%s/agents/%s"+
			"/endpoint/protocols/invocations?api-version=%s",
		cfg.Account, url.PathEscape(cfg.Project), url.PathEscape(agentName), apiVersion)
}

// endpointLinks wraps the invocations URL as resource links, or nil when any
// part of it is unknown. An absent link is better than one that does not
// resolve.
func endpointLinks(cfg *Config, agentName string) []deploy.ResourceLink {
	return adaptersdk.Link("Invocations endpoint", invocationsURL(cfg, agentName), "endpoint")
}
