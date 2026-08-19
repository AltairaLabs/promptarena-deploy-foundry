package foundry

import (
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
)

func basePlanInput() *planInput {
	return &planInput{
		AgentName:  "support-pack",
		Members:    []string{"main"},
		Prior:      newState(),
		PackHash:   "ph",
		ConfigHash: "ch",
		Delivery:   PackDelivery{Inline: true, SizeBytes: 100},
	}
}

// findChange returns the first change of the given resource type.
func findChange(changes []deploy.ResourceChange, resType string) *deploy.ResourceChange {
	for i := range changes {
		if changes[i].Type == resType {
			return &changes[i]
		}
	}
	return nil
}

func TestPlanFirstDeployCreatesAgentAndVersion(t *testing.T) {
	got := buildPlan(basePlanInput())

	agent := findChange(got.Changes, ResTypeAgent)
	if agent == nil || agent.Action != deploy.ActionCreate {
		t.Errorf("agent change = %+v, want a create", agent)
	}
	if agent != nil && agent.Name != "support-pack" {
		t.Errorf("agent name = %q, want support-pack", agent.Name)
	}

	version := findChange(got.Changes, ResTypeAgentVersion)
	if version == nil || version.Action != deploy.ActionCreate {
		t.Errorf("version change = %+v, want a create", version)
	}

	served := findChange(got.Changes, ResTypeServedVersion)
	if served == nil || served.Action != deploy.ActionCreate {
		t.Errorf("served version change = %+v, want a create", served)
	}
}

// An unchanged pack and config must produce no version: versions are
// immutable and billed, so creating one per apply would be pure waste.
func TestPlanNoChangeWhenHashesMatch(t *testing.T) {
	in := basePlanInput()
	in.Prior = &State{
		Version: StateVersion, AgentName: "support-pack",
		PackHash: "ph", ConfigHash: "ch", ServedVersion: "1",
	}

	got := buildPlan(in)

	agent := findChange(got.Changes, ResTypeAgent)
	if agent == nil || agent.Action != deploy.ActionNoChange {
		t.Errorf("agent change = %+v, want no-change", agent)
	}
	if version := findChange(got.Changes, ResTypeAgentVersion); version != nil {
		t.Errorf("version change = %+v, want none when nothing moved", version)
	}
	if got.Summary != "No changes" {
		t.Errorf("Summary = %q, want \"No changes\"", got.Summary)
	}
}

func TestPlanCreatesVersionWhenHashesMove(t *testing.T) {
	tests := []struct {
		name       string
		prior      *State
		wantDetail string
	}{
		{
			name: "pack changed",
			prior: &State{Version: StateVersion, AgentName: "support-pack",
				PackHash: "old", ConfigHash: "ch", ServedVersion: "1"},
			wantDetail: "pack changed",
		},
		{
			name: "config changed",
			prior: &State{Version: StateVersion, AgentName: "support-pack",
				PackHash: "ph", ConfigHash: "old", ServedVersion: "1"},
			wantDetail: "config changed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := basePlanInput()
			in.Prior = tt.prior

			got := buildPlan(in)

			version := findChange(got.Changes, ResTypeAgentVersion)
			if version == nil || version.Action != deploy.ActionCreate {
				t.Fatalf("version change = %+v, want a create", version)
			}
			if version.Detail != tt.wantDetail {
				t.Errorf("Detail = %q, want %q", version.Detail, tt.wantDetail)
			}
			// The agent already exists; only the version is new.
			if agent := findChange(got.Changes, ResTypeAgent); agent.Action != deploy.ActionNoChange {
				t.Errorf("agent action = %v, want no-change", agent.Action)
			}
		})
	}
}

// A version left mid-creation must be reconciled, not abandoned alongside a
// second version created for the same hashes.
func TestPlanReconcilesAnInFlightVersion(t *testing.T) {
	in := basePlanInput()
	in.Prior = &State{
		Version: StateVersion, AgentName: "support-pack",
		PackHash: "ph", ConfigHash: "ch", ServedVersion: "1",
		InFlight: &InFlightVersion{Version: "2", PackHash: "ph", ConfigHash: "ch"},
	}

	got := buildPlan(in)

	version := findChange(got.Changes, ResTypeAgentVersion)
	if version == nil || version.Action != deploy.ActionUpdate {
		t.Fatalf("version change = %+v, want an update", version)
	}
	if !containsSubstring([]string{version.Detail}, "did not finish") {
		t.Errorf("Detail = %q, want it to explain the reconcile", version.Detail)
	}
}

func TestPlanStagesAnOversizedPack(t *testing.T) {
	in := basePlanInput()
	in.Delivery = PackDelivery{Inline: false, SizeBytes: 40000}

	got := buildPlan(in)

	if findChange(got.Changes, ResTypePackObject) == nil {
		t.Error("no pack_object change, want the staged pack planned")
	}
	if !containsSubstring(got.Warnings, "over the inline limit") {
		t.Errorf("Warnings = %v, want one about staging", got.Warnings)
	}
}

// These are remote HTTP calls to another agent, so serving the whole pack from
// one Foundry agent does not resolve them.
func TestPlanWarnsAboutA2ATools(t *testing.T) {
	in := basePlanInput()
	in.HasA2ATools = true

	if got := buildPlan(in); !containsSubstring(got.Warnings, "a2a__") {
		t.Errorf("Warnings = %v, want one about a2a tools", got.Warnings)
	}
}

// Voice needs headroom Microsoft explicitly recommends; 0.5 vCPU will drop
// audio rather than fail outright, which is worse.
func TestPlanWarnsAboutVoiceAtHalfACPU(t *testing.T) {
	in := basePlanInput()
	in.CPU = "0.5"
	in.Protocols = []string{ProtocolInvocationsWS}

	if got := buildPlan(in); !containsSubstring(got.Warnings, "invocations_ws") {
		t.Errorf("Warnings = %v, want one about voice sizing", got.Warnings)
	}
}

func TestPlanNoVoiceWarningAtOneCPU(t *testing.T) {
	in := basePlanInput()
	in.CPU = "1"
	in.Protocols = []string{ProtocolInvocationsWS}

	if got := buildPlan(in); containsSubstring(got.Warnings, "invocations_ws") {
		t.Errorf("Warnings = %v, want no voice sizing warning at 1 vCPU", got.Warnings)
	}
}

// One agent serves every member in-process, so this is informational, not the
// "no routing between them" warning vertex has to give.
func TestPlanReportsMultiMemberPacks(t *testing.T) {
	in := basePlanInput()
	in.Members = []string{"billing", "triage"}

	got := buildPlan(in)

	version := findChange(got.Changes, ResTypeAgentVersion)
	if version == nil || !containsSubstring([]string{version.Detail}, "2 agents") {
		t.Errorf("version detail = %+v, want it to mention the member count", version)
	}
}

// Drift explains why an agent the user believes exists is being created.
func TestPlanSurfacesDriftFirst(t *testing.T) {
	in := basePlanInput()
	in.Drift = []string{"agent support-pack is in state but no longer exists"}

	got := buildPlan(in)

	if len(got.Warnings) == 0 || got.Warnings[0] != in.Drift[0] {
		t.Errorf("Warnings = %v, want drift reported first", got.Warnings)
	}
}

func TestPlanSummaryCountsActions(t *testing.T) {
	got := buildPlan(basePlanInput())

	if !containsSubstring([]string{got.Summary}, "to create") {
		t.Errorf("Summary = %q, want a create count", got.Summary)
	}
}
