package foundry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/AltairaLabs/PromptKit/runtime/deploy"
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
	// Drift describes resources that were in prior state but no longer exist,
	// as found by verifying against the live control plane.
	Drift []string
}

// buildPlan diffs desired against prior state and returns the resource changes.
// It performs no I/O: everything it needs has already been gathered.
func buildPlan(in *planInput) *deploy.PlanResponse {
	changes := []deploy.ResourceChange{agentChange(in)}

	if versionChange, ok := versionChange(in); ok {
		changes = append(changes, versionChange, servedVersionChange(in))
	}

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
		Warnings: planWarnings(in),
	}
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
			"Previous creation of version %s did not finish; reconcile it",
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
	// Drift first: it explains why a resource the user believes is deployed is
	// being created rather than updated.
	warnings := slices.Clone(in.Drift)

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
// create, update, delete and unchanged.
const summaryPartCount = 4

// summarizeChanges renders a one-line summary of the plan.
func summarizeChanges(changes []deploy.ResourceChange) string {
	var create, update, del, unchanged int
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
			update++
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
