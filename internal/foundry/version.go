package foundry

// Build metadata, overridden at link time by .goreleaser.yml with
// -ldflags="-X github.com/AltairaLabs/promptarena-deploy-foundry/internal/foundry.Version=v1.2.3".
var (
	// Version is the adapter build version.
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// Date is the build timestamp.
	Date = "unknown"
)
