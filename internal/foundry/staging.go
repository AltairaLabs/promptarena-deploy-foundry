package foundry

import (
	"context"
	"fmt"
	"strings"
)

// stagedPackBlobSuffix is appended to the pack hash to form the blob name.
const stagedPackBlobSuffix = ".json"

// stagedPackURI is where a pack of this hash lives in the staging container.
//
// The name is content-addressed so re-applying an unchanged pack targets the
// same blob, and two packs that differ cannot collide on one name.
func stagedPackURI(container, packHash string) string {
	return strings.TrimSuffix(container, "/") + "/" + packHash + stagedPackBlobSuffix
}

// stagePack uploads the pack when it is too large to inline, returning the URI
// the agent should read it from. An inline pack stages nothing.
//
// A pack whose hash already matches what prior state staged is not re-uploaded:
// the blob is content-addressed, so the bytes there are already the bytes this
// deploy would write.
func stagePack(
	ctx context.Context, client foundryClient, in *planContext, packJSON string,
) (string, error) {
	if in.Delivery.Inline {
		return "", nil
	}

	// validatePackDelivery rejects this at plan time, so reaching it means a
	// caller assembled a planContext by hand.
	if in.Cfg.StagingContainer == "" {
		return "", fmt.Errorf(
			"the pack is %d bytes, over the inline limit, but no staging_container is configured",
			in.Delivery.SizeBytes)
	}

	uri := stagedPackURI(in.Cfg.StagingContainer, in.PackHash)
	if in.Prior != nil && in.Prior.StagedPackURI == uri {
		return uri, nil
	}

	if err := client.StageObject(ctx, uri, []byte(packJSON)); err != nil {
		return "", fmt.Errorf("stage pack to %s: %w", uri, err)
	}
	return uri, nil
}

// validatePackDelivery rejects a pack that cannot be delivered.
//
// This runs at plan time. Without it the plan lists a pack_object resource and
// the apply then fails while building the spec — the plan promising a resource
// the apply cannot create, which is exactly the failure a plan exists to
// prevent.
func validatePackDelivery(delivery PackDelivery, cfg *Config) []string {
	if delivery.Inline || cfg.StagingContainer != "" {
		return nil
	}
	return []string{fmt.Sprintf(
		"the pack is %d bytes, over the %d byte inline limit, so it must be staged; "+
			"set staging_container to a blob container URL, or raise pack_inline_limit_bytes",
		delivery.SizeBytes, effectiveInlineLimit(cfg))}
}

// effectiveInlineLimit is the limit actually in force, resolving the default.
func effectiveInlineLimit(cfg *Config) int {
	if cfg.PackInlineLimitBytes <= 0 {
		return DefaultPackInlineLimitBytes
	}
	return cfg.PackInlineLimitBytes
}
