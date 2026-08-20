package foundry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseStateEmptyIsFirstDeploy(t *testing.T) {
	s, err := parseState("")
	if err != nil {
		t.Fatalf("parseState: %v", err)
	}
	if s.Version != StateVersion {
		t.Errorf("Version = %d, want %d", s.Version, StateVersion)
	}
	if s.AgentName != "" {
		t.Errorf("AgentName = %q, want empty on a first deploy", s.AgentName)
	}
}

func TestParseStateRejectsMalformedJSON(t *testing.T) {
	if _, err := parseState(`{`); err == nil {
		t.Fatal("parseState accepted malformed JSON")
	}
}

// State written by a newer adapter may carry fields this one cannot honor, so
// refuse it rather than silently dropping them.
func TestParseStateRejectsNewerStateVersion(t *testing.T) {
	_, err := parseState(`{"version":999}`)
	if err == nil {
		t.Fatal("parseState accepted a state version from the future")
	}
	if !strings.Contains(err.Error(), "upgrade the adapter") {
		t.Errorf("error = %v, want it to tell the user to upgrade", err)
	}
}

func TestStateRoundTrip(t *testing.T) {
	in := &State{
		Version:       StateVersion,
		PackHash:      "ph",
		ConfigHash:    "ch",
		AgentName:     "support-pack",
		ServedVersion: "3",
		PriorVersions: []string{"2", "1"},
	}

	encoded, err := in.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := parseState(encoded)
	if err != nil {
		t.Fatalf("parseState: %v", err)
	}

	if out.AgentName != in.AgentName || out.ServedVersion != in.ServedVersion {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
	if len(out.PriorVersions) != 2 || out.PriorVersions[0] != "2" {
		t.Errorf("PriorVersions = %v, want [2 1]", out.PriorVersions)
	}
}

// A newer adapter's fields must survive being read and rewritten by this one,
// or an older adapter in the loop silently truncates state it does not own.
func TestStatePreservesUnknownFields(t *testing.T) {
	raw := `{"version":1,"agent_name":"a","future_field":{"nested":true}}`

	s, err := parseState(raw)
	if err != nil {
		t.Fatalf("parseState: %v", err)
	}
	encoded, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("marshaled state is not valid JSON: %v", err)
	}
	if _, ok := decoded["future_field"]; !ok {
		t.Errorf("Marshal() = %s, want future_field preserved", encoded)
	}
	if _, ok := decoded["agent_name"]; !ok {
		t.Errorf("Marshal() = %s, want agent_name still present", encoded)
	}
}
