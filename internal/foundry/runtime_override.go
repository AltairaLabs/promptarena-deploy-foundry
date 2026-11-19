package foundry

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/AltairaLabs/promptarena/v2/deploy"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// Environment names the platform reserves or injects. The adapter itself sets
// PROMPTPACK_* and, when tracing overrides it, OTEL_EXPORTER_OTLP_ENDPOINT.
const (
	reservedPrefixPromptPack = "PROMPTPACK_"
	reservedPrefixAgent      = "AGENT_"
	reservedPrefixFoundry    = "FOUNDRY_"
	envPort                  = "PORT"
)

// runtimeOverrideSupport is what a hosted agent definition can apply of a
// runtime override.
//
// The definition's container_configuration carries an image and nothing else,
// so the image's own entrypoint always runs: command and args are refused.
// Secret environment is supported through project connections (see
// secretPlaceholder).
var runtimeOverrideSupport = adaptersdk.RuntimeOverrideSupport{
	SecretEnv: true,
	ReservedEnvPrefixes: []string{
		reservedPrefixPromptPack, reservedPrefixAgent, reservedPrefixFoundry,
	},
	// PORT is injected by the platform and is where it routes; the OTLP
	// endpoint is set from observability.
	ReservedEnvNames: []string{envPort, envOTLPEndpoint},
}

// connectionPartPattern is what a project connection name or credential key
// may contain. Anything else could not be addressed by the platform's
// ${{connections.<name>.credentials.<key>}} placeholder, and a placeholder that
// does not resolve leaves the variable empty with no error to notice.
var connectionPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// defaultCredentialKey is the credential field of an ApiKey connection, used
// when a secret ref names a connection without a key.
const defaultCredentialKey = "key"

// image is the image the agent version runs: the override's, when it names one.
func (c *Config) image() string {
	if c.RuntimeOverride != nil && c.RuntimeOverride.Image != "" {
		return c.RuntimeOverride.Image
	}
	return c.Image
}

// replacesRuntime reports whether the override swaps in another image. An
// override that only adds environment still runs the bundled runtime.
func (c *Config) replacesRuntime() bool {
	return c.RuntimeOverride != nil && c.RuntimeOverride.Image != ""
}

// validateRuntimeOverride checks the override against what a hosted agent can
// apply, then checks each secret ref addresses a project connection.
func (c *Config) validateRuntimeOverride() []string {
	o := c.RuntimeOverride
	if o == nil {
		return nil
	}
	errs := adaptersdk.ValidateRuntimeOverride(o, runtimeOverrideSupport)
	for _, name := range sortedSecretEnvNames(o.SecretEnv) {
		errs = append(errs, validateSecretRef(name, o.SecretEnv[name])...)
	}
	return errs
}

// validateSecretRef checks one secret_env entry. An empty name is already
// reported by the SDK validator.
func validateSecretRef(envName string, ref deploy.SecretRef) []string {
	var errs []string
	if ref.Version != "" {
		errs = append(errs, fmt.Sprintf(
			"runtime_override.secret_env %q sets version %q: Foundry project connections are not "+
				"versioned; the current value is read each time a session starts", envName, ref.Version))
	}
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return errs
	}
	connection, key := splitSecretName(name)
	if !connectionPartPattern.MatchString(connection) || !connectionPartPattern.MatchString(key) {
		errs = append(errs, fmt.Sprintf(
			"runtime_override.secret_env %q name %q must be <connection> or <connection>/<credential-key>, "+
				"each letters, digits, '-' or '_'", envName, ref.Name))
	}
	return errs
}

// splitSecretName splits "<connection>/<key>" into its parts. A bare
// connection name addresses the connection's "key" credential, which is the
// field an ApiKey connection stores its secret under.
func splitSecretName(name string) (connection, key string) {
	connection, key, found := strings.Cut(name, "/")
	if !found {
		return connection, defaultCredentialKey
	}
	return connection, key
}

// secretPlaceholder renders a secret ref as the placeholder Foundry resolves
// from a project connection when the session's container starts. Only the
// placeholder is stored in the agent definition, never the secret.
func secretPlaceholder(ref deploy.SecretRef) string {
	connection, key := splitSecretName(strings.TrimSpace(ref.Name))
	return fmt.Sprintf("${{connections.%s.credentials.%s}}", connection, key)
}

// addRuntimeOverrideEnv merges the override's environment into env, and hands
// a replacement container the agent it serves.
//
// The bundled runtime is deliberately not given PROMPTPACK_AGENT (it serves the
// whole pack, entry first), including under an env-only override. A
// replacement image is a different program that the SDK's handoff contract
// promises an agent name, so it gets the entry. Names were validated with the
// config (reserved prefixes, no handoff names), so nothing here can displace a
// variable the adapter set.
func addRuntimeOverrideEnv(env map[string]string, o *deploy.RuntimeOverride, entryAgent string) {
	if o == nil {
		return
	}
	for name, value := range o.Env {
		env[name] = value
	}
	for name, ref := range o.SecretEnv {
		env[name] = secretPlaceholder(ref)
	}
	if o.Image != "" {
		env[envAgentName] = entryAgent
	}
}

// sortedSecretEnvNames returns the variable names of a secret env map, sorted
// so validation errors come out in a stable order.
func sortedSecretEnvNames(m map[string]deploy.SecretRef) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
