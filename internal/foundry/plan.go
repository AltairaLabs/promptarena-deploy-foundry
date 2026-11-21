package foundry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// Resource type names surfaced in plans.
const (
	// ResTypeAgent is the Foundry agent itself — one per pack.
	ResTypeAgent = "agent"
	// ResTypeAgentVersion is one immutable version of that agent.
	ResTypeAgentVersion = "agent_version"
	// ResTypeServedVersion is the endpoint selector pointing at a version.
	ResTypeServedVersion = "served_version"
	// ResTypePackObject is the pack staged to Azure Blob storage.
	ResTypePackObject = "pack_object"
)

// stagedPackName is the plan-facing name of the staged pack object.
const stagedPackName = "pack.json"

// servedVersionName is the plan-facing name of the endpoint selector.
const servedVersionName = "endpoint selector"

// voiceMinCPU is the smallest cpu allocation Microsoft recommends for
// real-time voice. Below it, audio degrades rather than failing outright.
const voiceMinCPU = "0.5"

// planInput is everything buildPlan needs, gathered by Provider.Plan.
type planInput struct {
	// AgentName is the sanitized agent name derived from the pack id.
	AgentName string
	// Members are the pack's agents, all served by the one Foundry agent.
	Members     []string
	Prior       *State
	PackHash    string
	ConfigHash  string
	CPU         string
	Protocols   []string
	Delivery    PackDelivery
	HasA2ATools bool
	// Stage is the rollout stage, one of the Rollout* values; empty is live.
	Stage string
	// Drift describes resources that were in prior state but no longer exist,
	// as found by verifying against the live control plane. They travel as
	// changes rather than warnings so they are counted and rendered like
	// everything else in the plan.
	Drift []deploy.ResourceChange
	// Advisories describe the verification itself rather than its result:
	// state that could not be checked, or an endpoint repointed out of band.
	// Neither is an absence, so neither is a change.
	Advisories []string
}

// buildPlan diffs desired against prior state and returns the resource changes.
// It performs no I/O: everything it needs has already been gathered.
func buildPlan(in *planInput) *deploy.PlanResponse {
	// Drift first: each entry explains why the resource below it is being
	// created rather than updated.
	changes := append([]deploy.ResourceChange{}, in.Drift...)
	changes = append(changes, agentChange(in))

	rollout, rolloutWarnings := rolloutChanges(in)
	changes = append(changes, rollout...)

	if !in.Delivery.Inline {
		changes = append(changes, deploy.ResourceChange{
			Type:   ResTypePackObject,
			Name:   stagedPackName,
			Action: deploy.ActionCreate,
			Detail: fmt.Sprintf("Stage the %d byte pack to Blob storage", in.Delivery.SizeBytes),
		})
	}

	return &deploy.PlanResponse{
		Changes:  changes,
		Summary:  summarizeChanges(changes),
		Warnings: append(planWarnings(in), rolloutWarnings...),
	}
}

// rolloutChanges plans the version and endpoint changes for the apply's
// rollout stage, along with any warning that the apply will refuse.
func rolloutChanges(in *planInput) (changes []deploy.ResourceChange, warnings []string) {
	switch in.Stage {
	case RolloutCandidate:
		return candidateChanges(in), nil
	case RolloutPromote:
		return promoteChanges(in)
	default:
		return liveChanges(in), nil
	}
}

// liveChanges plans a version that is created and served at once.
func liveChanges(in *planInput) []deploy.ResourceChange {
	var changes []deploy.ResourceChange
	if change, ok := versionChange(in); ok {
		changes = append(changes, change, servedVersionChange(in))
	} else if served, ok := servedUnchanged(in); ok {
		changes = append(changes, served)
	}
	return appendStagedUnchanged(changes, in)
}

// candidateChanges plans a version that is created and staged, not served.
func candidateChanges(in *planInput) []deploy.ResourceChange {
	var served []deploy.ResourceChange
	if change, ok := servedUnchanged(in); ok {
		served = append(served, change)
	}
	if stagedFor(in.Prior, in.PackHash, in.ConfigHash) ||
		servesFor(in.Prior, in.PackHash, in.ConfigHash) {
		return appendStagedUnchanged(served, in)
	}

	stagedAction := deploy.ActionCreate
	stagedText := "Record the new version as staged; the endpoint does not move"
	if in.Prior.StagedVersion != nil {
		stagedAction = deploy.ActionUpdate
		stagedText = fmt.Sprintf("Replace staged version %s with the new version; the endpoint does not move",
			in.Prior.StagedVersion.Version)
	}
	changes := []deploy.ResourceChange{
		{
			Type:   ResTypeAgentVersion,
			Name:   in.AgentName,
			Action: deploy.ActionCreate,
			Detail: "Create a version to stage without serving it" + memberSuffix(in),
		},
		{
			Type:   ResTypeStagedVersion,
			Name:   stagedVersionName,
			Action: stagedAction,
			Detail: stagedText,
		},
	}
	return append(changes, served...)
}

// promoteChanges plans serving the staged version. It creates no version.
func promoteChanges(in *planInput) (changes []deploy.ResourceChange, warnings []string) {
	if stagedFor(in.Prior, in.PackHash, in.ConfigHash) {
		return []deploy.ResourceChange{{
			Type:   ResTypeServedVersion,
			Name:   servedVersionName,
			Action: deploy.ActionUpdate,
			Detail: fmt.Sprintf("Point the endpoint at staged version %s at 100%% traffic",
				in.Prior.StagedVersion.Version),
		}}, nil
	}
	if servesFor(in.Prior, in.PackHash, in.ConfigHash) {
		served, _ := servedUnchanged(in)
		return []deploy.ResourceChange{served}, nil
	}
	return appendStagedUnchanged(nil, in), []string{promoteRefusal(in.Prior).Error()}
}

// servedUnchanged reports the version the endpoint keeps serving, if any.
func servedUnchanged(in *planInput) (deploy.ResourceChange, bool) {
	if in.Prior.ServedVersion == "" {
		return deploy.ResourceChange{}, false
	}
	return deploy.ResourceChange{
		Type:   ResTypeServedVersion,
		Name:   servedVersionName,
		Action: deploy.ActionNoChange,
		Detail: fmt.Sprintf("Serving version %s", in.Prior.ServedVersion),
	}, true
}

// appendStagedUnchanged adds the staged version, when one is recorded, as a
// resource this plan leaves alone.
func appendStagedUnchanged(changes []deploy.ResourceChange, in *planInput) []deploy.ResourceChange {
	staged := in.Prior.StagedVersion
	if staged == nil {
		return changes
	}
	return append(changes, deploy.ResourceChange{
		Type:   ResTypeStagedVersion,
		Name:   stagedVersionName,
		Action: deploy.ActionNoChange,
		Detail: fmt.Sprintf("Version %s is staged and not served", staged.Version),
	})
}

// agentChange decides whether the agent shell itself must be created. The agent
// is created once and then persists; only its versions churn.
func agentChange(in *planInput) deploy.ResourceChange {
	if in.Prior.AgentName == "" {
		return deploy.ResourceChange{
			Type:   ResTypeAgent,
			Name:   in.AgentName,
			Action: deploy.ActionCreate,
			Detail: "Create the Foundry hosted agent",
		}
	}
	return deploy.ResourceChange{
		Type:   ResTypeAgent,
		Name:   in.AgentName,
		Action: deploy.ActionNoChange,
		Detail: "Up to date",
	}
}

// versionChange decides whether a new version is needed, and why. It reports
// false when nothing moved — versions are immutable and billed, so creating one
// per apply would be waste, not caution.
func versionChange(in *planInput) (change deploy.ResourceChange, needed bool) {
	detail, needed := versionReason(in)
	if !needed {
		return deploy.ResourceChange{}, false
	}

	action := deploy.ActionCreate
	if in.Prior.InFlight != nil {
		action = deploy.ActionUpdate
	}

	return deploy.ResourceChange{
		Type:   ResTypeAgentVersion,
		Name:   in.AgentName,
		Action: action,
		Detail: detail + memberSuffix(in),
	}, true
}

// versionReason explains why a version is needed, or reports that none is.
func versionReason(in *planInput) (reason string, needed bool) {
	if in.Prior.InFlight != nil {
		return fmt.Sprintf(
			"Previous creation of version %s did not finish; create a new version",
			in.Prior.InFlight.Version), true
	}
	if in.Prior.ServedVersion == "" {
		return "Create the first agent version", true
	}

	var reasons []string
	if in.Prior.PackHash != in.PackHash {
		reasons = append(reasons, "pack changed")
	}
	if in.Prior.ConfigHash != in.ConfigHash {
		reasons = append(reasons, "config changed")
	}
	if len(reasons) == 0 {
		return "", false
	}
	return strings.Join(reasons, ", "), true
}

// memberSuffix notes how many pack members the version will serve. One agent
// serves them all, routed in-process, so this is informational.
func memberSuffix(in *planInput) string {
	if len(in.Members) <= 1 {
		return ""
	}
	return fmt.Sprintf(" (%d agents routed in-process)", len(in.Members))
}

// servedVersionChange is the selector update that follows every new version.
// Traffic splitting is unsupported, so this is always a single 100% rule.
func servedVersionChange(in *planInput) deploy.ResourceChange {
	action := deploy.ActionUpdate
	detail := "Point the endpoint at the new version at 100% traffic"
	if in.Prior.ServedVersion == "" {
		action = deploy.ActionCreate
		detail = "Point the endpoint at the first version at 100% traffic"
	}

	return deploy.ResourceChange{
		Type:   ResTypeServedVersion,
		Name:   servedVersionName,
		Action: action,
		Detail: detail,
	}
}

// planWarnings returns advisories about a plan that will apply cleanly but may
// not behave as the author expects.
func planWarnings(in *planInput) []string {
	// Advisories first: they explain that the plan below may be built on
	// state nobody could confirm.
	warnings := slices.Clone(in.Advisories)

	if in.HasA2ATools {
		warnings = append(warnings,
			"the pack declares a2a__ tools; those are remote calls to another agent over HTTP, "+
				"so serving the whole pack from one Foundry agent does not resolve them and "+
				"they will fail at runtime")
	}

	if in.CPU == voiceMinCPU && slices.Contains(in.Protocols, ProtocolInvocationsWS) {
		warnings = append(warnings, fmt.Sprintf(
			"%s is declared at %s vCPU; Microsoft recommends at least 1 vCPU / 2 GiB for "+
				"real-time voice, and below that audio degrades rather than failing outright",
			ProtocolInvocationsWS, voiceMinCPU))
	}

	if !in.Delivery.Inline {
		warnings = append(warnings, fmt.Sprintf(
			"the pack is %d bytes, over the inline limit, so it is delivered through Blob "+
				"storage; the agent's managed identity needs read access to the staging container",
			in.Delivery.SizeBytes))
	}

	return warnings
}

// summaryPartCount is the number of action buckets a summary can mention:
// create, update, delete, drifted and unchanged.
const summaryPartCount = 5

// summarizeChanges renders a one-line summary of the plan.
func summarizeChanges(changes []deploy.ResourceChange) string {
	var create, update, del, unchanged, drifted int
	for i := range changes {
		switch changes[i].Action {
		case deploy.ActionCreate:
			create++
		case deploy.ActionUpdate:
			update++
		case deploy.ActionDelete:
			del++
		case deploy.ActionNoChange:
			unchanged++
		case deploy.ActionDrift:
			drifted++
		}
	}

	parts := make([]string, 0, summaryPartCount)
	if create > 0 {
		parts = append(parts, fmt.Sprintf("%d to create", create))
	}
	if update > 0 {
		parts = append(parts, fmt.Sprintf("%d to update", update))
	}
	if del > 0 {
		parts = append(parts, fmt.Sprintf("%d to delete", del))
	}
	if drifted > 0 {
		parts = append(parts, fmt.Sprintf("%d drifted", drifted))
	}

	// "1 unchanged" alone is not a change set; report it as no changes so the
	// CLI's own no-op handling reads correctly.
	if len(parts) == 0 {
		return "No changes"
	}
	if unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", unchanged))
	}
	return strings.Join(parts, ", ")
}
