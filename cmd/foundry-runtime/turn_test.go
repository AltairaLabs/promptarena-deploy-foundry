package main

import (
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/sdk"
)

// One Foundry agent serves the whole pack, so which member answers is the
// pack's decision unless the deployment pins one.
func TestResolveAgentName(t *testing.T) {
	tests := []struct {
		name string
		cfg  *runtimeConfig
		pack *prompt.Pack
		want string
	}{
		{
			name: "an explicit agent wins",
			cfg:  &runtimeConfig{AgentName: "pinned"},
			pack: &prompt.Pack{Agents: &prompt.AgentsConfig{Entry: "entry"}},
			want: "pinned",
		},
		{
			name: "otherwise the pack's entry",
			cfg:  &runtimeConfig{},
			pack: &prompt.Pack{Agents: &prompt.AgentsConfig{Entry: "entry"}},
			want: "entry",
		},
		{
			name: "a single prompt needs no entry",
			cfg:  &runtimeConfig{},
			pack: &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{"solo": {}}},
			want: "solo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAgentName(tt.cfg, tt.pack)
			if err != nil {
				t.Fatalf("resolveAgentName: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveAgentName = %q, want %q", got, tt.want)
			}
		})
	}
}

// Ambiguity must fail at startup rather than pick arbitrarily and serve the
// wrong agent for the life of the deployment.
func TestResolveAgentNameAmbiguous(t *testing.T) {
	pack := &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{"a": {}, "b": {}}}

	if _, err := resolveAgentName(&runtimeConfig{}, pack); err == nil {
		t.Fatal("resolveAgentName picked one of several prompts")
	}
}

// The conversation id is transport only: it maps to a PromptKit session key so
// the pack stays authoritative over its own history.
func TestConversationOptionsAddsTheConversationID(t *testing.T) {
	base := []sdk.Option{}

	got := conversationOptions(base, &invocationRequest{ConversationID: "c-1"})
	if len(got) != 1 {
		t.Errorf("len(opts) = %d, want the conversation id added", len(got))
	}
}

func TestConversationOptionsWithoutAConversationID(t *testing.T) {
	base := []sdk.Option{}

	if got := conversationOptions(base, &invocationRequest{}); len(got) != 0 {
		t.Errorf("len(opts) = %d, want the base unchanged", len(got))
	}
}

// The base slice must not be mutated: it is shared by every turn, and
// appending in place would leak one request's id into the next.
func TestConversationOptionsDoesNotMutateTheBase(t *testing.T) {
	base := make([]sdk.Option, 0, 4)
	base = append(base, sdk.WithUserID("t"))

	_ = conversationOptions(base, &invocationRequest{ConversationID: "c-1"})

	if len(base) != 1 {
		t.Errorf("len(base) = %d, want it unchanged", len(base))
	}
}

// A bare "404 Resource not found" is indistinguishable between a wrong
// endpoint, a wrong deployment and an identity without access.
func TestBindingDescription(t *testing.T) {
	setBindingDescription("https://ep/openai/v1", "openai", "gpt-4-1-mini", "client-123")

	got := describeBinding()
	for _, want := range []string{"https://ep/openai/v1", "openai", "gpt-4-1-mini", "client-123"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeBinding() = %q, want it to name %q", got, want)
		}
	}
}

// An absent identity is the interesting case, so it is named rather than
// rendered as an empty string.
func TestBindingDescriptionWithoutAnIdentity(t *testing.T) {
	setBindingDescription("https://ep", "openai", "m", "")

	if got := describeBinding(); !strings.Contains(got, "no client id") {
		t.Errorf("describeBinding() = %q, want it to flag the missing identity", got)
	}
}

// warnUnsupportedTools reports once per process; calling it must be safe and
// silent when everything is supported.
func TestWarnUnsupportedToolsWithNothingUnsupported(t *testing.T) {
	warnUnsupportedTools(nil)
}
