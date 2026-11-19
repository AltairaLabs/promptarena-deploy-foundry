package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// The CLI saves the state a failed apply returns in place of the stored one,
// because the adapter advertises partial_apply_state. That state must still
// describe every resource the prior state did, plus any the apply created, or
// the CLI forgets resources that exist.
func TestFailedApplyReturnsPriorResourcesAndCreatedOnes(t *testing.T) {
	const changedPack = `{"id": "solo-pack", "prompts": {"main": {"system": "changed"}}}`
	const deployedPrior = `{"version":1,"agent_name":"solo-pack","served_version":"2",` +
		`"prior_versions":["1"],"pack_hash":"old-pack","config_hash":"old-config","future_field":"kept"}`

	tests := []struct {
		name  string
		prior string
		seed  bool
		fail  func(*recordingClient)
		// wantPrior is true when the prior agent, served version and hashes
		// must come back untouched.
		wantPrior bool
	}{
		{
			name:      "version creation fails on an existing agent",
			prior:     deployedPrior,
			seed:      true,
			fail:      func(c *recordingClient) { c.createVersionErr = errors.New("quota exceeded") },
			wantPrior: true,
		},
		{
			name:      "endpoint update fails on an existing agent",
			prior:     deployedPrior,
			seed:      true,
			fail:      func(c *recordingClient) { c.servedVersionErr = errors.New("endpoint refused") },
			wantPrior: true,
		},
		{
			name:  "version creation fails after the agent is created",
			prior: `{"version":1,"future_field":"kept"}`,
			fail:  func(c *recordingClient) { c.createVersionErr = errors.New("quota exceeded") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newRecordingClient()
			if tt.seed {
				client.seedAgent("solo-pack", "2")
			}
			tt.fail(client)
			p := applyProvider(t, client)

			raw, err := p.Apply(context.Background(), applyRequest(validConfigJSON, changedPack, tt.prior), nil)
			if err == nil {
				t.Fatal("Apply succeeded, want the injected failure")
			}
			if raw == "" {
				t.Fatal("Apply returned no state with its error")
			}

			got, parseErr := parseState(raw)
			if parseErr != nil {
				t.Fatalf("parseState: %v", parseErr)
			}

			// Whether it existed before or this apply created it, the agent
			// must be recorded: it exists in the project either way.
			if got.AgentName != "solo-pack" {
				t.Errorf("AgentName = %q, want solo-pack", got.AgentName)
			}

			var fields map[string]json.RawMessage
			if jsonErr := json.Unmarshal([]byte(raw), &fields); jsonErr != nil {
				t.Fatalf("state is not a JSON object: %v", jsonErr)
			}
			if string(fields["future_field"]) != `"kept"` {
				t.Errorf("future_field = %s, want the prior state's unknown field kept", fields["future_field"])
			}

			if !tt.wantPrior {
				return
			}
			// The failed apply served nothing new, so the prior served version,
			// its history and the hashes describing it must survive. Advancing
			// the hashes would make the next plan report "up to date".
			if got.ServedVersion != "2" {
				t.Errorf("ServedVersion = %q, want the prior 2", got.ServedVersion)
			}
			if !slices.Equal(got.PriorVersions, []string{"1"}) {
				t.Errorf("PriorVersions = %v, want [1]", got.PriorVersions)
			}
			if got.PackHash != "old-pack" || got.ConfigHash != "old-config" {
				t.Errorf("hashes = %q/%q, want the prior old-pack/old-config", got.PackHash, got.ConfigHash)
			}
		})
	}
}
