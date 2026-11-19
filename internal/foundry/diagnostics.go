package foundry

import (
	"fmt"
	"slices"
	"strings"
)

// acrHostSuffix is the Azure Container Registry login-server suffix.
const acrHostSuffix = ".azurecr.io"

// runtimeServedProtocols are the contracts the bundled
// promptkit-foundry-runtime image currently implements. Declaring a protocol
// the container does not serve produces an agent the platform routes to and
// gets nothing back from.
var runtimeServedProtocols = []string{ProtocolInvocations}

// diagnoseConfig returns non-blocking advisories about a structurally valid
// config. These catch misconfigurations that would otherwise surface as an
// opaque failure minutes into an apply.
func diagnoseConfig(cfg *Config) []string {
	var warnings []string

	warnings = append(warnings, diagnoseImage(cfg)...)

	warnings = append(warnings, diagnoseProtocols(cfg)...)
	warnings = append(warnings, diagnoseRuntimeOverride(cfg)...)
	warnings = append(warnings, diagnoseObservability(cfg)...)

	return warnings
}

// diagnoseImage checks the image is somewhere Foundry can actually pull from.
func diagnoseImage(cfg *Config) []string {
	image := cfg.image()
	if image == "" {
		return nil
	}

	registry, _, found := strings.Cut(image, "/")
	if !found {
		registry = image
	}
	// Strip any port before matching the host suffix.
	if host, _, hasPort := strings.Cut(registry, ":"); hasPort {
		registry = host
	}

	if !strings.HasSuffix(registry, acrHostSuffix) {
		return []string{fmt.Sprintf(
			"image %q is not in an Azure Container Registry; Foundry pulls only from ACR, so "+
				"mirror the image into your own registry and reference that instead",
			image)}
	}
	return nil
}

// diagnoseProtocols warns about contracts the bundled runtime does not serve.
// A replacement image serves whatever it serves, which the adapter cannot know.
func diagnoseProtocols(cfg *Config) []string {
	if cfg.RuntimeOverride != nil && cfg.RuntimeOverride.Image != "" {
		return nil
	}
	var unserved []string
	for _, p := range cfg.Protocols {
		if !slices.Contains(runtimeServedProtocols, p) {
			unserved = append(unserved, p)
		}
	}
	if len(unserved) == 0 {
		return nil
	}

	return []string{fmt.Sprintf(
		"the bundled promptkit-foundry-runtime image does not serve %s yet; declaring it "+
			"means the platform will route requests the container cannot answer. Remove it, "+
			"or point image at a container of your own that implements it",
		strings.Join(unserved, ", "))}
}

// diagnoseObservability notes that Foundry supplies a tracing endpoint itself.
func diagnoseObservability(cfg *Config) []string {
	if cfg.Observability == nil || cfg.Observability.OTLPEndpoint == "" {
		return nil
	}
	return []string{
		"observability.otlp_endpoint overrides the OTEL_EXPORTER_OTLP_ENDPOINT that Foundry " +
			"injects; leave it unset to use the platform's own collector",
	}
}

// diagnoseRuntimeOverride says what the platform does with a replacement
// container, which differs from what an operator used to a long-running
// service might expect.
func diagnoseRuntimeOverride(cfg *Config) []string {
	if !cfg.hasRuntimeOverride() {
		return nil
	}
	return []string{fmt.Sprintf(
		"runtime_override: Foundry starts the container only when a session is created, routes the "+
			"declared protocols (%s) to it on the injected PORT, and reports the version active once "+
			"the image pulls, whether or not the container serves anything",
		strings.Join(cfg.Protocols, ", "))}
}
