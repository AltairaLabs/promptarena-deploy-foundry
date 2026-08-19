package foundry

import (
	"fmt"
	"sort"
	"strings"
)

// Agent name limits.
//
// Agent names are path segments in the data-plane REST API
// (POST /agents/{name}/versions), and Microsoft documents neither the legal
// character set nor the length limit. This sanitizer therefore targets a
// conservative subset — lowercase alphanumerics and interior hyphens, starting
// with a letter — that is almost certainly within whatever the API accepts.
// Widen it once a real deploy settles the question, never before.
const (
	maxAgentNameLength = 63
	// fallbackAgentName is used when sanitizing leaves nothing behind, so a pack
	// with an unusual id still produces a deployable name instead of an empty
	// path segment.
	fallbackAgentName = "agent"
	// namePrefixLetter prefixes a name that would otherwise start with a digit.
	namePrefixLetter = "a"
)

// Azure tag limits.
const (
	maxTagNameLength  = 512
	maxTagValueLength = 256
)

// reservedTagChars are rejected by Azure Resource Manager in tag names.
const reservedTagChars = `<>%&\?/`

// sanitizeAgentName converts an arbitrary string into a legal agent name.
//
// It is deterministic and lossy: two different inputs can collapse to the same
// name. That is safe here because one pack maps to exactly one agent, so there
// is never a second name to collide with — unlike vertex's label sanitizer,
// which has to reject collisions across many resources.
func sanitizeAgentName(s string) string {
	name := strings.Trim(foldAgentNameChars(s), "-")
	if name == "" {
		return fallbackAgentName
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = namePrefixLetter + name
	}

	if len(name) > maxAgentNameLength {
		// Re-trim after truncation: cutting mid-name can expose a hyphen that
		// the sanitizer refuses to emit anywhere else.
		name = strings.TrimRight(name[:maxAgentNameLength], "-")
	}
	if name == "" {
		return fallbackAgentName
	}
	return name
}

// foldAgentNameChars lowercases s, keeps alphanumerics, folds the common
// separators to a single hyphen, and drops everything else. Runs of separators
// collapse so the result never contains "--".
func foldAgentNameChars(s string) string {
	var b strings.Builder
	lastHyphen := false

	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || r == '.' || r == ' ':
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		default:
			// Anything else is dropped.
		}
	}

	return b.String()
}

// validateTags checks user-supplied tags against the limits Azure Resource
// Manager enforces. Errors are emitted in sorted key order so a config with
// several bad tags produces a stable message list.
func validateTags(tags map[string]string) []string {
	if len(tags) == 0 {
		return nil
	}

	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []string
	for _, name := range names {
		errs = append(errs, validateTag(name, tags[name])...)
	}
	return errs
}

// validateTag checks one tag name and value.
func validateTag(name, value string) []string {
	if name == "" {
		return []string{"tag name must not be empty"}
	}

	var errs []string
	if len(name) > maxTagNameLength {
		errs = append(errs, fmt.Sprintf(
			"tag name %q is %d characters, which exceeds the %d character limit",
			truncateForMessage(name), len(name), maxTagNameLength))
	}
	if len(value) > maxTagValueLength {
		errs = append(errs, fmt.Sprintf(
			"tag %q has a %d character value, which exceeds the %d character limit",
			name, len(value), maxTagValueLength))
	}
	if strings.ContainsAny(name, reservedTagChars) {
		errs = append(errs, fmt.Sprintf(
			"tag name %q must not contain any of %s", name, reservedTagChars))
	}
	return errs
}

// messageNameLimit bounds how much of an over-long name is echoed back, so one
// bad tag cannot flood the validation output.
const messageNameLimit = 40

// truncateForMessage shortens a string for inclusion in an error message.
func truncateForMessage(s string) string {
	if len(s) <= messageNameLimit {
		return s
	}
	return s[:messageNameLimit] + "..."
}
