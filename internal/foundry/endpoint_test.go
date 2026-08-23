package foundry

import (
	"strings"
	"testing"
)

// The URL is pinned to a literal because it is the one thing a user copies out
// of a deploy and runs. It matches what scripts/invoke.sh builds and what the
// integration suite POSTs to successfully — drift here hands out an address
// that 404s, which is worse than showing nothing.
func TestInvocationsURL(t *testing.T) {
	got := invocationsURL(&Config{Account: "acct", Project: "proj"}, "my-agent")
	want := "https://acct.services.ai.azure.com/api/projects/proj/agents/my-agent" +
		"/endpoint/protocols/invocations?api-version=v1"

	if got != want {
		t.Errorf("invocationsURL =\n  %q\nwant\n  %q", got, want)
	}
}

// An absent link beats one that cannot resolve, so anything missing yields no
// URL at all rather than a half-built one.
func TestInvocationsURLNeedsEveryPart(t *testing.T) {
	tests := []struct {
		name  string
		cfg   *Config
		agent string
	}{
		{"nil config", nil, "a"},
		{"no account", &Config{Project: "p"}, "a"},
		{"no project", &Config{Account: "acct"}, "a"},
		{"no agent", &Config{Account: "acct", Project: "p"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := invocationsURL(tt.cfg, tt.agent); got != "" {
				t.Errorf("invocationsURL = %q, want empty", got)
			}
			if links := endpointLinks(tt.cfg, tt.agent); links != nil {
				t.Errorf("endpointLinks = %+v, want nil", links)
			}
		})
	}
}

// A name with a slash or space would otherwise split the path or break the URL.
func TestInvocationsURLEscapes(t *testing.T) {
	got := invocationsURL(&Config{Account: "acct", Project: "my proj"}, "a/b")
	if strings.Contains(got, "my proj") || strings.Contains(got, "agents/a/b/") {
		t.Errorf("invocationsURL did not escape its path segments: %q", got)
	}
}

func TestEndpointLinksShape(t *testing.T) {
	links := endpointLinks(&Config{Account: "acct", Project: "proj"}, "my-agent")
	if len(links) != 1 {
		t.Fatalf("endpointLinks = %+v, want exactly one link", links)
	}
	if links[0].Rel != "endpoint" {
		t.Errorf("Rel = %q, want endpoint", links[0].Rel)
	}
	if links[0].Label == "" {
		t.Error("a link with no label renders as a bare URL")
	}
}
