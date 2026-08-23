package foundry

import "testing"

// Foundry pulls only from Azure Container Registry. A ghcr.io reference is
// accepted at create time and then fails when the container cannot be pulled,
// so it is worth catching before the deploy.
func TestDiagnoseConfigWarnsAboutNonACRImages(t *testing.T) {
	cfg := &Config{Account: "a", Project: "p", Image: "ghcr.io/altairalabs/promptkit-foundry-runtime:v1"}

	if got := diagnoseConfig(cfg); !containsSubstring(got, "Azure Container Registry") {
		t.Errorf("diagnoseConfig() = %v, want a registry warning", got)
	}
}

func TestDiagnoseConfigQuietForACRImages(t *testing.T) {
	cfg := &Config{
		Account: "a", Project: "p",
		Image: "myacr.azurecr.io/altairalabs/promptkit-foundry-runtime:v1",
		CPU:   "1", Memory: "2Gi",
	}

	if got := diagnoseConfig(cfg); containsSubstring(got, "Azure Container Registry") {
		t.Errorf("diagnoseConfig() = %v, want no registry warning", got)
	}
}

// Unset sizing is an error now, not a warning: the old advisory told the user
// "the platform's default sizing applies", and Foundry has no default.
func TestDiagnoseConfigDoesNotClaimADefaultSizing(t *testing.T) {
	cfg := &Config{Account: "a", Project: "p", Image: "acr.azurecr.io/x:1"}

	if got := diagnoseConfig(cfg); containsSubstring(got, "default sizing") {
		t.Errorf("diagnoseConfig() = %v, want no claim of a platform default", got)
	}
}

// The bundled runtime image serves /readiness and /invocations today. Declaring
// a contract it does not implement produces an agent the platform will route to
// and get nothing back from.
func TestDiagnoseConfigWarnsAboutUnimplementedProtocols(t *testing.T) {
	cfg := &Config{
		Account: "a", Project: "p", Image: "acr.azurecr.io/x:1",
		CPU: "1", Memory: "2Gi",
		Protocols: []string{ProtocolInvocations, ProtocolResponses},
	}

	if got := diagnoseConfig(cfg); !containsSubstring(got, ProtocolResponses) {
		t.Errorf("diagnoseConfig() = %v, want a warning about responses", got)
	}
}

func TestDiagnoseConfigQuietForImplementedProtocols(t *testing.T) {
	cfg := &Config{
		Account: "a", Project: "p", Image: "acr.azurecr.io/x:1",
		CPU: "1", Memory: "2Gi",
		Protocols: []string{ProtocolInvocations},
	}

	if got := diagnoseConfig(cfg); len(got) != 0 {
		t.Errorf("diagnoseConfig() = %v, want no warnings", got)
	}
}

// Foundry injects OTEL_EXPORTER_OTLP_ENDPOINT, so an explicit endpoint is an
// override — worth saying, since vertex requires one and the habit carries over.
func TestDiagnoseConfigNotesAnOTLPOverride(t *testing.T) {
	cfg := &Config{
		Account: "a", Project: "p", Image: "acr.azurecr.io/x:1",
		CPU: "1", Memory: "2Gi",
		Protocols:     []string{ProtocolInvocations},
		Observability: &Observability{TracingEnabled: true, OTLPEndpoint: "https://collector:4318"},
	}

	if got := diagnoseConfig(cfg); !containsSubstring(got, "overrides") {
		t.Errorf("diagnoseConfig() = %v, want a note about the override", got)
	}
}
