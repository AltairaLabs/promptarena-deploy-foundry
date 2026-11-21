package foundry

import (
	"context"
	"errors"
	"fmt"

	"github.com/AltairaLabs/promptarena/v2/deploy"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// Resource names for the staged version.
const (
	// ResTypeStagedVersion is a version created by a candidate apply that the
	// endpoint does not serve.
	ResTypeStagedVersion = "staged_version"
	// stagedVersionName is the plan-facing name of the staged version record.
	stagedVersionName = "staged version"
)

// servesFor reports whether the served version was built from these hashes,
// with nothing in flight. A staged version never counts: only the served
// version's hashes are recorded at the top level of state.
func servesFor(state *State, packHash, configHash string) bool {
	return state.ServedVersion != "" && state.InFlight == nil &&
		state.PackHash == packHash && state.ConfigHash == configHash
}

// stagedFor reports whether the staged version was built from these hashes.
func stagedFor(state *State, packHash, configHash string) bool {
	staged := state.StagedVersion
	return staged != nil && staged.PackHash == packHash && staged.ConfigHash == configHash
}

// applyCandidate creates a version and records it as staged, leaving the
// endpoint where it is.
//
// It creates nothing when a version already runs this pack and config, staged
// or served: versions are immutable and billed, and a second identical
// candidate would only be another version to tell apart.
func applyCandidate(
	ctx context.Context, client foundryClient, in *applyInput,
	prior, state *State, report *adaptersdk.ProgressReporter,
) (*State, error) {
	if state.ServedVersion == "" {
		// With nothing served there is no version for the endpoint to stay
		// on, and what the platform routes to before the first selector patch
		// is not something this adapter controls.
		return prior, fmt.Errorf("rollout_stage %s: %w; deploy with rollout_stage %s first",
			RolloutCandidate, ErrNothingServed, RolloutLive)
	}
	if stagedFor(state, in.PackHash, in.ConfigHash) {
		reportProgress(report, fmt.Sprintf(
			"Version %s is already staged for this pack", state.StagedVersion.Version), progressServed)
		return state, nil
	}
	if servesFor(state, in.PackHash, in.ConfigHash) {
		reportProgress(report, fmt.Sprintf(
			"Version %s already serves this pack; nothing to stage", state.ServedVersion), progressServed)
		return state, nil
	}

	version, err := createVersion(ctx, client, in)
	if version == nil {
		return unlessUnchanged(prior, state), err
	}

	// A version still provisioning is staged all the same: it exists, and
	// promote checks it is active before serving it.
	state.StagedVersion = &StagedVersion{
		Version:     version.Version,
		PackHash:    in.PackHash,
		ConfigHash:  in.ConfigHash,
		PackVersion: in.Spec.Metadata[metaPackVersion],
	}
	reportResource(report, ResTypeStagedVersion, version.Version,
		deploy.ActionCreate, "created", stagedDetail(state))
	if err != nil {
		return state, err
	}
	reportProgress(report, "Version staged", progressServed)
	return state, nil
}

// stagedDetail describes the staged version against what is being served.
func stagedDetail(state *State) string {
	if state.ServedVersion == "" {
		return fmt.Sprintf("Version %s is staged; no version is served yet", state.StagedVersion.Version)
	}
	return fmt.Sprintf("Version %s is staged; the endpoint still serves version %s",
		state.StagedVersion.Version, state.ServedVersion)
}

// applyPromote points the endpoint at the staged version. It creates nothing.
//
// The staged version must have been built from this apply's pack and config,
// or state would record hashes the served version does not run and the next
// plan would miss the difference. A promote with nothing to do — the served
// version already runs this pack — succeeds, so repeating one is harmless.
func applyPromote(
	ctx context.Context, client foundryClient, in *applyInput,
	prior, state *State, report *adaptersdk.ProgressReporter,
) (*State, error) {
	// Served first: a live apply may have served this pack since it was
	// staged, and promoting the older staged version would move the endpoint
	// backwards to a version that runs the same thing.
	if servesFor(state, in.PackHash, in.ConfigHash) {
		reportProgress(report, fmt.Sprintf(
			"Version %s already serves this pack", state.ServedVersion), progressServed)
		return state, nil
	}
	if !stagedFor(state, in.PackHash, in.ConfigHash) {
		return prior, promoteRefusal(state)
	}

	staged := *state.StagedVersion
	if err := confirmStaged(ctx, client, in.AgentName, staged.Version); err != nil {
		if errors.Is(err, ErrVersionNotFound) || errors.Is(err, ErrVersionFailed) {
			// The record points at nothing that will ever run. Dropping it
			// lets the next candidate apply stage a version instead of
			// reporting this one as already staged.
			state.StagedVersion = nil
			return state, err
		}
		return prior, err
	}

	if err := client.SetEndpoint(ctx, in.AgentName, staged.Version, in.Cfg.Protocols); err != nil {
		return prior, fmt.Errorf("configure endpoint of agent %q for staged version %q: %w",
			in.AgentName, staged.Version, err)
	}

	markServed(state, staged.Version, staged.PackHash, staged.ConfigHash, staged.PackVersion)
	state.StagedVersion = nil
	reportServed(report, in, staged.Version)
	return state, nil
}

// promoteRefusal explains why there is nothing to promote.
func promoteRefusal(state *State) error {
	if state.StagedVersion == nil {
		return fmt.Errorf("rollout_stage %s: %w; apply with rollout_stage %s first",
			RolloutPromote, ErrNothingStaged, RolloutCandidate)
	}
	return fmt.Errorf("rollout_stage %s: staged version %s: %w; apply this pack with rollout_stage %s",
		RolloutPromote, state.StagedVersion.Version, ErrStagedMismatch, RolloutCandidate)
}

// confirmStaged checks against the platform that the staged version exists
// and is active. State alone is not enough: a version deleted or failed since
// it was staged would leave the endpoint pointing at nothing that runs.
func confirmStaged(ctx context.Context, client foundryClient, agent, version string) error {
	versions, err := client.ListVersions(ctx, agent)
	if err != nil {
		return fmt.Errorf("list versions of agent %q: %w", agent, err)
	}
	for i := range versions {
		if versions[i].Version != version {
			continue
		}
		switch versions[i].Status {
		case VersionStatusActive:
		case VersionStatusFailed, VersionStatusDeleting, VersionStatusDeleted:
			return fmt.Errorf("staged version %q of agent %q is %s: %w; apply with rollout_stage %s to stage another",
				version, agent, versions[i].Status, ErrVersionFailed, RolloutCandidate)
		default:
			return fmt.Errorf("agent %q version %q is %s: %w",
				agent, version, versions[i].Status, ErrStagedNotActive)
		}
		return nil
	}
	return fmt.Errorf("staged version %q of agent %q: %w; apply with rollout_stage %s to stage another",
		version, agent, ErrVersionNotFound, RolloutCandidate)
}
