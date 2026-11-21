package foundry

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// Packs with a version field, so pack_version metadata has a value to carry.
const (
	versionedPack     = `{"id": "solo-pack", "version": "v1.2.0", "prompts": {"main": {}}}`
	versionedPackNext = `{"id": "solo-pack", "version": "v1.3.0", "prompts": {"main": {"system": "x"}}}`
)

// stageConfig returns the valid config with rollout_stage set.
func stageConfig(stage string) string {
	return strings.Replace(validConfigJSON, `"cpu": "1",`, `"cpu": "1", "rollout_stage": "`+stage+`",`, 1)
}

// applyState runs one apply and returns the parsed state alongside the raw one.
func applyState(
	t *testing.T, p *Provider, cfg, pack, prior string,
) (raw string, state *State, err error) {
	t.Helper()
	raw, err = p.Apply(context.Background(), applyRequest(cfg, pack, prior), nil)
	if raw == "" {
		return raw, nil, err
	}
	parsed, parseErr := parseState(raw)
	if parseErr != nil {
		t.Fatalf("parseState: %v", parseErr)
	}
	return raw, parsed, err
}

// servedLive deploys versionedPack live and returns its state.
func servedLive(t *testing.T, p *Provider) string {
	t.Helper()
	raw, _, err := applyState(t, p, validConfigJSON, versionedPack, "")
	if err != nil {
		t.Fatalf("live Apply: %v", err)
	}
	return raw
}

func TestValidateRolloutStage(t *testing.T) {
	for _, stage := range []string{RolloutLive, RolloutCandidate, RolloutPromote} {
		got, err := NewProvider().ValidateConfig(context.Background(),
			&deploy.ValidateRequest{Config: stageConfig(stage)})
		if err != nil || !got.Valid {
			t.Errorf("stage %q: Valid = %v, errors %v, err %v", stage, got.Valid, got.Errors, err)
		}
	}

	got, err := NewProvider().ValidateConfig(context.Background(),
		&deploy.ValidateRequest{Config: stageConfig("canary")})
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if got.Valid || !containsSubstring(got.Errors, "rollout_stage") {
		t.Errorf("Valid = %v, Errors = %v, want rollout_stage rejected", got.Valid, got.Errors)
	}
}

func TestSchemaDeclaresRolloutStage(t *testing.T) {
	if !strings.Contains(configSchema, `"rollout_stage"`) ||
		!strings.Contains(configSchema, `"candidate"`) || !strings.Contains(configSchema, `"promote"`) {
		t.Error("config schema does not declare rollout_stage with its values")
	}
}

// Moving from candidate to promote must not read as a config change, or
// promote would refuse every staged version.
func TestRolloutStageIsNotHashed(t *testing.T) {
	hashes := map[string]bool{}
	for _, stage := range []string{"", RolloutCandidate, RolloutPromote} {
		cfg := validConfigJSON
		if stage != "" {
			cfg = stageConfig(stage)
		}
		in, err := gatherPlanInput(applyRequest(cfg, versionedPack, ""))
		if err != nil {
			t.Fatalf("gatherPlanInput: %v", err)
		}
		hashes[in.ConfigHash] = true
	}
	if len(hashes) != 1 {
		t.Errorf("config hash differs across rollout stages: %v", hashes)
	}
}

func TestVersionMetadataCarriesPackVersion(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	servedLive(t, p)

	versions, err := client.ListVersions(context.Background(), "solo-pack")
	if err != nil || len(versions) != 1 {
		t.Fatalf("ListVersions = %v, %v; want one version", versions, err)
	}
	if got := versions[0].Metadata[metaPackVersion]; got != "v1.2.0" {
		t.Errorf("pack_version = %q, want v1.2.0", got)
	}
}

func TestBuildTagsPackVersion(t *testing.T) {
	cfg := &Config{Tags: map[string]string{metaPackVersion: "user-value", "team": "a"}}

	tags := buildTags(cfg, "pack", "v2")
	if tags[metaPackVersion] != "v2" {
		t.Errorf("pack_version = %q, want the pack's version to win over a user tag", tags[metaPackVersion])
	}
	if tags["team"] != "a" {
		t.Error("user tag was dropped")
	}

	if _, ok := buildTags(&Config{}, "pack", "")[metaPackVersion]; ok {
		t.Error("pack_version set for a pack with no version")
	}
}

func TestCandidateStagesWithoutServing(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)

	client.calls = nil
	_, state, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}

	if want := []string{"CreateVersion"}; !slices.Equal(client.calls, want) {
		t.Errorf("calls = %v, want %v: a candidate must not move the endpoint", client.calls, want)
	}
	if state.ServedVersion != "1" {
		t.Errorf("ServedVersion = %q, want 1 still served", state.ServedVersion)
	}
	if state.InFlight != nil {
		t.Errorf("InFlight = %+v, want a staged version not recorded as in-flight", state.InFlight)
	}
	staged := state.StagedVersion
	if staged == nil || staged.Version != "2" || staged.PackVersion != "v1.3.0" {
		t.Fatalf("StagedVersion = %+v, want version 2 for pack v1.3.0", staged)
	}
	// The top-level hashes belong to the served version.
	priorState, _ := parseState(prior)
	if state.PackHash != priorState.PackHash {
		t.Error("PackHash moved to the staged pack; the next live plan would miss the change")
	}
	if agent, _ := client.GetAgent(context.Background(), "solo-pack"); agent.ServedVersion != "1" {
		t.Errorf("platform serves %q, want 1", agent.ServedVersion)
	}
}

func TestCandidateIsIdempotent(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)

	staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}

	client.calls = nil
	if _, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, staged); err != nil {
		t.Fatalf("second candidate Apply: %v", err)
	}
	if len(client.calls) != 0 {
		t.Errorf("calls = %v, want none for a pack already staged", client.calls)
	}

	client.calls = nil
	_, state, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPack, prior)
	if err != nil {
		t.Fatalf("candidate of the served pack: %v", err)
	}
	if len(client.calls) != 0 || state.StagedVersion != nil {
		t.Errorf("calls = %v, staged = %+v; want nothing staged for the served pack",
			client.calls, state.StagedVersion)
	}
}

func TestCandidateReplacesAStaleStagedVersion(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)

	staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}
	third := `{"id": "solo-pack", "version": "v1.4.0", "prompts": {"main": {"system": "y"}}}`
	_, state, err := applyState(t, p, stageConfig(RolloutCandidate), third, staged)
	if err != nil {
		t.Fatalf("second candidate Apply: %v", err)
	}
	if state.StagedVersion == nil || state.StagedVersion.Version != "3" {
		t.Errorf("StagedVersion = %+v, want version 3", state.StagedVersion)
	}
}

// The first deploy can be a candidate: the agent is created, nothing served.
func TestCandidateFirstDeploy(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	_, state, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPack, "")
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}
	if want := []string{"CreateAgent", "CreateVersion"}; !slices.Equal(client.calls, want) {
		t.Errorf("calls = %v, want %v", client.calls, want)
	}
	if state.ServedVersion != "" || state.StagedVersion == nil {
		t.Errorf("served %q staged %+v, want only a staged version", state.ServedVersion, state.StagedVersion)
	}
}

func TestCandidateRecordsAVersionStillCreating(t *testing.T) {
	client := newRecordingClient()
	client.versionStatus = VersionStatusCreating
	p := applyProvider(t, client)

	_, state, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPack, "")
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}
	if state.StagedVersion == nil || state.InFlight != nil {
		t.Errorf("staged %+v in-flight %+v, want it staged", state.StagedVersion, state.InFlight)
	}
}

func TestCandidateCreateFailures(t *testing.T) {
	t.Run("gave up waiting", func(t *testing.T) {
		client := newRecordingClient()
		p := applyProvider(t, client)
		prior := servedLive(t, p)
		client.createVersionErr = errors.New("still creating")
		client.failedVersion = &AgentVersion{Version: "2", Status: VersionStatusCreating}

		_, state, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
		if err == nil {
			t.Fatal("Apply succeeded, want the create error")
		}
		if state.StagedVersion == nil || state.StagedVersion.Version != "2" {
			t.Errorf("StagedVersion = %+v, want the version that exists recorded", state.StagedVersion)
		}
	})

	t.Run("failed outright", func(t *testing.T) {
		client := newRecordingClient()
		p := applyProvider(t, client)
		prior := servedLive(t, p)
		client.createVersionErr = errors.New("boom")

		raw, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
		if err == nil {
			t.Fatal("Apply succeeded, want the create error")
		}
		if raw != prior {
			t.Error("state changed for a candidate that created nothing")
		}
	})
}

// A staged version is not served, so a live apply of the same pack must still
// create and serve a version rather than read the pack as deployed.
func TestLiveDoesNotMistakeStagedForServed(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)
	staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}

	client.calls = nil
	_, state, err := applyState(t, p, validConfigJSON, versionedPackNext, staged)
	if err != nil {
		t.Fatalf("live Apply: %v", err)
	}
	if want := []string{"CreateVersion", "SetEndpoint"}; !slices.Equal(client.calls, want) {
		t.Errorf("calls = %v, want %v", client.calls, want)
	}
	if state.ServedVersion != "3" || state.PackVersion != "v1.3.0" {
		t.Errorf("served %q pack version %q, want 3 and v1.3.0", state.ServedVersion, state.PackVersion)
	}
}

func TestPromoteServesTheStagedVersion(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)
	staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}
	stagedState, _ := parseState(staged)

	client.calls = nil
	promoted, state, err := applyState(t, p, stageConfig(RolloutPromote), versionedPackNext, staged)
	if err != nil {
		t.Fatalf("promote Apply: %v", err)
	}
	if want := []string{"ListVersions", "SetEndpoint"}; !slices.Equal(client.calls, want) {
		t.Errorf("calls = %v, want %v: promote creates nothing", client.calls, want)
	}
	if state.ServedVersion != "2" || state.StagedVersion != nil {
		t.Errorf("served %q staged %+v, want 2 served and nothing staged", state.ServedVersion, state.StagedVersion)
	}
	if state.PackHash != stagedState.StagedVersion.PackHash || state.PackVersion != "v1.3.0" {
		t.Error("served hashes are not the staged version's")
	}
	if !slices.Equal(state.PriorVersions, []string{"1"}) {
		t.Errorf("PriorVersions = %v, want [1]", state.PriorVersions)
	}
	if agent, _ := client.GetAgent(context.Background(), "solo-pack"); agent.ServedVersion != "2" {
		t.Errorf("platform serves %q, want 2", agent.ServedVersion)
	}

	// Promote again, then a live apply of the same pack: both are no-ops.
	for _, cfg := range []string{stageConfig(RolloutPromote), validConfigJSON} {
		client.calls = nil
		if _, _, err := applyState(t, p, cfg, versionedPackNext, promoted); err != nil {
			t.Fatalf("repeat Apply: %v", err)
		}
		if len(client.calls) != 0 {
			t.Errorf("calls = %v, want none once the staged version is served", client.calls)
		}
	}
}

func TestPromoteRefusals(t *testing.T) {
	tests := []struct {
		name    string
		pack    string
		setup   func(c *recordingClient)
		want    error
		cleared bool
	}{
		{name: "different pack", pack: versionedPack, want: ErrStagedMismatch},
		{name: "deleted on the platform", pack: versionedPackNext, want: ErrVersionNotFound, cleared: true,
			setup: func(c *recordingClient) { c.versions["solo-pack"] = c.versions["solo-pack"][:1] }},
		{name: "not active", pack: versionedPackNext, want: ErrStagedNotActive,
			setup: func(c *recordingClient) { c.listedStatus = VersionStatusFailed }},
		{name: "list fails", pack: versionedPackNext, want: ErrProjectNotFound,
			setup: func(c *recordingClient) { c.listVersionsErr = ErrProjectNotFound }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newRecordingClient()
			p := applyProvider(t, client)
			// Serve a third pack so the refused pack is not the served one.
			first, _, err := applyState(t, p, validConfigJSON,
				`{"id": "solo-pack", "version": "v0", "prompts": {"main": {"system": "z"}}}`, "")
			if err != nil {
				t.Fatalf("live Apply: %v", err)
			}
			staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, first)
			if err != nil {
				t.Fatalf("candidate Apply: %v", err)
			}
			if tt.setup != nil {
				tt.setup(client)
			}

			client.calls = nil
			raw, state, err := applyState(t, p, stageConfig(RolloutPromote), tt.pack, staged)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if slices.Contains(client.calls, "SetEndpoint") {
				t.Error("promote moved the endpoint after refusing")
			}
			if tt.cleared {
				if state.StagedVersion != nil {
					t.Errorf("StagedVersion = %+v, want a missing version dropped", state.StagedVersion)
				}
				return
			}
			if raw != staged {
				t.Error("state changed for a refused promote")
			}
		})
	}
}

func TestPromoteWithNothingStaged(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)

	if _, err := p.Apply(context.Background(),
		applyRequest(stageConfig(RolloutPromote), versionedPack, ""), nil); !errors.Is(err, ErrNothingStaged) {
		t.Fatalf("err = %v, want ErrNothingStaged", err)
	}
	if len(client.calls) != 0 {
		t.Errorf("calls = %v, want promote to create nothing, the agent included", client.calls)
	}

	prior := servedLive(t, p)
	raw, _, err := applyState(t, p, stageConfig(RolloutPromote), versionedPackNext, prior)
	if !errors.Is(err, ErrNothingStaged) || raw != prior {
		t.Errorf("err = %v, state changed = %v; want ErrNothingStaged and the state untouched", err, raw != prior)
	}
}

func TestPromoteEndpointFailureKeepsState(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)
	staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}
	client.servedVersionErr = errors.New("throttled")

	raw, _, err := applyState(t, p, stageConfig(RolloutPromote), versionedPackNext, staged)
	if err == nil || raw != staged {
		t.Errorf("err = %v, state changed = %v; want the error and the staged state kept", err, raw != staged)
	}
}

func TestStatusReportsServedAndStaged(t *testing.T) {
	client := newRecordingClient()
	p := applyProvider(t, client)
	prior := servedLive(t, p)
	staged, _, err := applyState(t, p, stageConfig(RolloutCandidate), versionedPackNext, prior)
	if err != nil {
		t.Fatalf("candidate Apply: %v", err)
	}

	got, err := p.Status(context.Background(), &deploy.StatusRequest{
		DeployConfig: validConfigJSON, PriorState: staged,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != StatusDeployed {
		t.Errorf("Status = %q, want deployed: a staged version is not a fault", got.Status)
	}
	details := make([]string, 0, len(got.Resources))
	for _, r := range got.Resources {
		details = append(details, r.Detail)
	}
	if !containsSubstring(details, "Serving version 1 (pack version v1.2.0)") {
		t.Errorf("details = %v, want the served version and its pack version", details)
	}
	if !containsSubstring(details, "Version 2 (pack version v1.3.0) is staged") {
		t.Errorf("details = %v, want the staged version", details)
	}
}

func TestStagedStatusRepointedOutOfBand(t *testing.T) {
	got := stagedStatus(&Agent{ServedVersion: "2"}, &StagedVersion{Version: "2"})
	if !strings.Contains(got.Detail, "outside this adapter") {
		t.Errorf("Detail = %q, want it to say the endpoint moved", got.Detail)
	}
	got = stagedStatus(&Agent{ServedVersion: "1"}, &StagedVersion{Version: "2"})
	if got.Detail != "Version 2 is staged and not served" {
		t.Errorf("Detail = %q", got.Detail)
	}
}

func TestStagedDetail(t *testing.T) {
	if got := stagedDetail(&State{StagedVersion: &StagedVersion{Version: "1"}}); !strings.Contains(got,
		"no version is served") {
		t.Errorf("detail = %q", got)
	}
}

func stagedPlanInput(stage string) *planInput {
	in := basePlanInput()
	in.Stage = stage
	in.Prior = &State{
		Version: StateVersion, AgentName: "support-pack",
		PackHash: "old", ConfigHash: "ch", ServedVersion: "1",
	}
	return in
}

func TestPlanCandidate(t *testing.T) {
	got := buildPlan(stagedPlanInput(RolloutCandidate))

	if v := findChange(got.Changes, ResTypeAgentVersion); v == nil || v.Action != deploy.ActionCreate {
		t.Errorf("version change = %+v, want a create", v)
	}
	if s := findChange(got.Changes, ResTypeStagedVersion); s == nil || s.Action != deploy.ActionCreate {
		t.Errorf("staged change = %+v, want a create", s)
	}
	if s := findChange(got.Changes, ResTypeServedVersion); s == nil || s.Action != deploy.ActionNoChange {
		t.Errorf("served change = %+v, want the endpoint left alone", s)
	}

	in := stagedPlanInput(RolloutCandidate)
	in.Prior.StagedVersion = &StagedVersion{Version: "2", PackHash: "other", ConfigHash: "ch"}
	if s := findChange(buildPlan(in).Changes, ResTypeStagedVersion); s == nil || s.Action != deploy.ActionUpdate {
		t.Errorf("staged change = %+v, want an update replacing the stale one", s)
	}

	in.Prior.StagedVersion.PackHash = "ph"
	got = buildPlan(in)
	if v := findChange(got.Changes, ResTypeAgentVersion); v != nil || got.Summary != "No changes" {
		t.Errorf("changes = %+v, summary %q; want nothing to do for a staged pack", got.Changes, got.Summary)
	}
}

func TestPlanPromote(t *testing.T) {
	in := stagedPlanInput(RolloutPromote)
	in.Prior.StagedVersion = &StagedVersion{Version: "2", PackHash: "ph", ConfigHash: "ch"}
	got := buildPlan(in)
	if v := findChange(got.Changes, ResTypeAgentVersion); v != nil {
		t.Errorf("version change = %+v, want promote to create none", v)
	}
	served := findChange(got.Changes, ResTypeServedVersion)
	if served == nil || served.Action != deploy.ActionUpdate || !strings.Contains(served.Detail, "staged version 2") {
		t.Errorf("served change = %+v, want the endpoint moved to version 2", served)
	}

	in.Prior.StagedVersion.PackHash = "other"
	got = buildPlan(in)
	if !containsSubstring(got.Warnings, "different pack") {
		t.Errorf("Warnings = %v, want the mismatch called out", got.Warnings)
	}

	in = stagedPlanInput(RolloutPromote)
	in.Prior.PackHash = "ph"
	got = buildPlan(in)
	if served := findChange(got.Changes, ResTypeServedVersion); served == nil || served.Action != deploy.ActionNoChange {
		t.Errorf("served change = %+v, want no change once served", served)
	}

	in = stagedPlanInput(RolloutPromote)
	if got := buildPlan(in); !containsSubstring(got.Warnings, "no version is staged") {
		t.Errorf("Warnings = %v, want nothing staged called out", got.Warnings)
	}
}

func TestPlanLiveShowsServedAndStaged(t *testing.T) {
	in := stagedPlanInput(RolloutLive)
	in.Prior.PackHash = "ph"
	in.Prior.StagedVersion = &StagedVersion{Version: "2"}
	got := buildPlan(in)

	if s := findChange(got.Changes, ResTypeServedVersion); s == nil || s.Detail != "Serving version 1" {
		t.Errorf("served change = %+v", s)
	}
	if s := findChange(got.Changes, ResTypeStagedVersion); s == nil || s.Action != deploy.ActionNoChange {
		t.Errorf("staged change = %+v", s)
	}
	if got.Summary != "No changes" {
		t.Errorf("Summary = %q, want No changes", got.Summary)
	}
}

func TestStagedVersionRoundTrips(t *testing.T) {
	in := &State{Version: StateVersion, StagedVersion: &StagedVersion{Version: "4", PackVersion: "v9"}}
	raw, err := in.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := parseState(raw)
	if err != nil || out.StagedVersion == nil || out.StagedVersion.Version != "4" {
		t.Fatalf("parsed %+v, %v", out, err)
	}
	if cleared := clearedState(out); cleared.StagedVersion != nil {
		t.Error("clearedState kept a staged version of an agent that is gone")
	}
}

func TestDryRunListVersions(t *testing.T) {
	c := newDryRunClient()
	if _, err := c.ListVersions(context.Background(), "a"); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want ErrAgentNotFound", err)
	}
	ctx := context.Background()
	if _, err := c.CreateAgent(ctx, &AgentSpec{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateVersion(ctx, "a", &AgentSpec{Metadata: map[string]string{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.ListVersions(ctx, "a")
	if err != nil || len(got) != 1 || got[0].Metadata["k"] != "v" {
		t.Errorf("ListVersions = %+v, %v", got, err)
	}
	// Creating a version serves nothing.
	if agent, _ := c.GetAgent(ctx, "a"); agent.ServedVersion != "" {
		t.Errorf("ServedVersion = %q, want empty until SetEndpoint", agent.ServedVersion)
	}
}
