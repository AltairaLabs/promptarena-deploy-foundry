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

	// ErrVersionNotFound reports that a specific agent version does not exist.
	ErrVersionNotFound = errors.New("agent version not found")

	// ErrVersionFailed reports that a version finished provisioning in the
	// failed state. The wrapped message carries the platform's reason.
	ErrVersionFailed = errors.New("agent version failed to provision")
)

// isAgentNotFound reports whether err means the agent does not exist, as
// opposed to any other lookup failure. A 403 or a throttle must never read as
// not-found: that would turn a transient problem into a spurious create.
func isAgentNotFound(err error) bool {
	return errors.Is(err, ErrAgentNotFound)
}
