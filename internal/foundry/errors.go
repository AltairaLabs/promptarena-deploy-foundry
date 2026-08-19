package foundry

import "errors"

// Sentinel errors. Callers match these with errors.Is rather than on message
// text, so the wording stays free to change.
var (
	// ErrNotImplemented marks a lifecycle operation that is not built out yet.
	ErrNotImplemented = errors.New("not implemented")

	// ErrAgentNotFound reports that a hosted agent recorded in adapter state no
	// longer exists on the Foundry control plane.
	ErrAgentNotFound = errors.New("agent not found")
)
