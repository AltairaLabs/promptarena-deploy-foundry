package foundry

import (
	"strings"
	"testing"
)

// Agent names are path segments in the data-plane REST API, so they are
// sanitized to a conservative subset rather than passed through. The legal set
// is not documented; this errs narrow deliberately.
func TestSanitizeAgentName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already legal", "support-bot", "support-bot"},
		{"uppercase is lowered", "SupportBot", "supportbot"},
		{"underscores and dots become hyphens", "support_bot.v2", "support-bot-v2"},
		{"spaces become hyphens", "support bot", "support-bot"},
		{"unsupported characters are dropped", "support!@#bot", "supportbot"},
		{"runs of separators collapse", "support___bot", "support-bot"},
		{"leading and trailing separators are trimmed", "--support-bot--", "support-bot"},
		{"a leading digit gets a letter prefix", "2fast", "a2fast"},
		{"empty input yields a stable fallback", "", fallbackAgentName},
		{"all-illegal input yields the fallback", "!!!", fallbackAgentName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAgentName(tt.in); got != tt.want {
				t.Errorf("sanitizeAgentName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeAgentNameTruncatesToTheLengthLimit(t *testing.T) {
	got := sanitizeAgentName(strings.Repeat("a", maxAgentNameLength+50))

	if len(got) != maxAgentNameLength {
		t.Errorf("len = %d, want %d", len(got), maxAgentNameLength)
	}
}

// Truncation must not leave a trailing hyphen — that is exactly the shape the
// sanitizer refuses to emit everywhere else.
func TestSanitizeAgentNameTruncationDoesNotLeaveATrailingHyphen(t *testing.T) {
	in := strings.Repeat("ab-", maxAgentNameLength)

	got := sanitizeAgentName(in)
	if strings.HasSuffix(got, "-") {
		t.Errorf("sanitizeAgentName(...) = %q, want no trailing hyphen", got)
	}
}

func TestSanitizeAgentNameIsDeterministic(t *testing.T) {
	const in = "My Pack_v2!"
	if a, b := sanitizeAgentName(in), sanitizeAgentName(in); a != b {
		t.Errorf("sanitizeAgentName is not deterministic: %q vs %q", a, b)
	}
}

func TestValidateTags(t *testing.T) {
	tests := []struct {
		name       string
		tags       map[string]string
		wantErrSub string
	}{
		{
			name:       "empty key is rejected",
			tags:       map[string]string{"": "v"},
			wantErrSub: "tag name must not be empty",
		},
		{
			name:       "over-long key is rejected",
			tags:       map[string]string{strings.Repeat("k", maxTagNameLength+1): "v"},
			wantErrSub: "exceeds the",
		},
		{
			name:       "over-long value is rejected",
			tags:       map[string]string{"k": strings.Repeat("v", maxTagValueLength+1)},
			wantErrSub: "exceeds the",
		},
		{
			name:       "reserved characters are rejected",
			tags:       map[string]string{"team<x>": "v"},
			wantErrSub: "must not contain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateTags(tt.tags)
			if !containsSubstring(errs, tt.wantErrSub) {
				t.Errorf("validateTags() = %v, want an error containing %q", errs, tt.wantErrSub)
			}
		})
	}
}

func TestValidateTagsAcceptsOrdinaryTags(t *testing.T) {
	tags := map[string]string{"team": "platform", "env": "prod", "cost-centre": "1234"}

	if errs := validateTags(tags); len(errs) != 0 {
		t.Errorf("validateTags() = %v, want no errors", errs)
	}
}

func TestValidateTagsAcceptsNil(t *testing.T) {
	if errs := validateTags(nil); len(errs) != 0 {
		t.Errorf("validateTags(nil) = %v, want no errors", errs)
	}
}

// Errors are emitted in sorted key order so a config with several bad tags
// produces the same message list on every run.
func TestValidateTagsIsOrderStable(t *testing.T) {
	tags := map[string]string{"z<": "v", "a<": "v", "m<": "v"}

	first := validateTags(tags)
	for range 20 {
		if got := validateTags(tags); !equalStrings(got, first) {
			t.Fatalf("validateTags is not order-stable: %v vs %v", got, first)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
