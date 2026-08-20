package foundry

import "testing"

const multiAgentPack = `{
  "id": "support-pack",
  "prompts": {"triage": {}, "billing": {}},
  "agents": {"entry": "triage", "members": {"triage": {}, "billing": {}}}
}`

const singleAgentPack = `{"id": "solo-pack", "prompts": {"main": {}}}`

func TestPackID(t *testing.T) {
	got, err := packID(singleAgentPack)
	if err != nil {
		t.Fatalf("packID: %v", err)
	}
	if got != "solo-pack" {
		t.Errorf("packID = %q, want solo-pack", got)
	}
}

// The pack id seeds the agent name, so a pack without one cannot be deployed.
func TestPackIDMissing(t *testing.T) {
	if _, err := packID(`{"prompts":{"main":{}}}`); err == nil {
		t.Fatal("packID accepted a pack with no id")
	}
}

func TestPackIDRejectsMalformedJSON(t *testing.T) {
	if _, err := packID(`{`); err == nil {
		t.Fatal("packID accepted malformed JSON")
	}
}

func TestPackMembersMultiAgent(t *testing.T) {
	members, entry, err := packMembers(multiAgentPack)
	if err != nil {
		t.Fatalf("packMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %v, want 2", members)
	}
	// Sorted, so plan output is stable across runs.
	if members[0] != "billing" || members[1] != "triage" {
		t.Errorf("members = %v, want them sorted", members)
	}
	if entry != "triage" {
		t.Errorf("entry = %q, want triage", entry)
	}
}

// A single-prompt pack has one implicit member, and it is the entry.
func TestPackMembersSingleAgent(t *testing.T) {
	members, entry, err := packMembers(singleAgentPack)
	if err != nil {
		t.Fatalf("packMembers: %v", err)
	}
	if len(members) != 1 || members[0] != "main" {
		t.Fatalf("members = %v, want [main]", members)
	}
	if entry != "main" {
		t.Errorf("entry = %q, want main", entry)
	}
}

// With several prompts but no agents block there is no declared entry; the
// runtime's own precedence picks one at request time.
func TestPackMembersMultiPromptWithoutAgentsBlock(t *testing.T) {
	members, entry, err := packMembers(`{"id":"p","prompts":{"a":{},"b":{}}}`)
	if err != nil {
		t.Fatalf("packMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %v, want 2", members)
	}
	if entry != "" {
		t.Errorf("entry = %q, want empty", entry)
	}
}

func TestPackMembersRejectsPackWithNoPrompts(t *testing.T) {
	if _, _, err := packMembers(`{"id":"p"}`); err == nil {
		t.Fatal("packMembers accepted a pack with nothing to serve")
	}
}

func TestHasA2ATools(t *testing.T) {
	tests := []struct {
		name string
		pack string
		want bool
	}{
		{"no tools", `{"id":"p","prompts":{"a":{}}}`, false},
		{"ordinary tools", `{"id":"p","tools":{"lookup":{}}}`, false},
		{"a2a tool", `{"id":"p","tools":{"a2a__billing__refund":{}}}`, true},
		{"malformed JSON reports false", `{`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasA2ATools(tt.pack); got != tt.want {
				t.Errorf("hasA2ATools() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecidePackDelivery(t *testing.T) {
	tests := []struct {
		name       string
		packJSON   string
		limit      int
		wantInline bool
	}{
		{"under the limit is inline", "abc", 10, true},
		{"exactly at the limit is still inline", "abcde", 5, true},
		{"over the limit is staged", "abcdef", 5, false},
		{"a zero limit falls back to the default", "abc", 0, true},
		{"a negative limit falls back to the default", "abc", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decidePackDelivery(tt.packJSON, &Config{PackInlineLimitBytes: tt.limit})
			if got.Inline != tt.wantInline {
				t.Errorf("Inline = %v, want %v", got.Inline, tt.wantInline)
			}
			if got.SizeBytes != len(tt.packJSON) {
				t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, len(tt.packJSON))
			}
		})
	}
}
