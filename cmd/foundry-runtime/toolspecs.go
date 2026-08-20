package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/AltairaLabs/PromptKit/sdk"
	sdktools "github.com/AltairaLabs/PromptKit/sdk/tools"
)

// Tool execution modes this runtime supports.
const (
	toolModeMock = "mock"
	toolModeLive = "live"
)

// toolHTTP is the HTTP binding for a live tool.
type toolHTTP struct {
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
}

// toolSpec is the executable subset of an arena tool definition. The compiled
// pack carries only a tool's schema, so execution config arrives separately
// through PROMPTPACK_TOOL_SPECS.
type toolSpec struct {
	Name         string    `json:"name"`
	Mode         string    `json:"mode"`
	MockResult   any       `json:"mock_result,omitempty"`
	MockTemplate string    `json:"mock_template,omitempty"`
	HTTP         *toolHTTP `json:"http,omitempty"`
}

// parseToolSpecs decodes the injected tool specs. An empty string yields no
// specs, which is the "pack declares no executable tools" case.
func parseToolSpecs(raw string) (map[string]toolSpec, error) {
	if raw == "" {
		return nil, nil
	}
	var specs map[string]toolSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", envToolSpecs, err)
	}
	return specs, nil
}

// mockHandler builds a handler for a mock tool. mock_template wins over
// mock_result when both are set, matching the arena's precedence.
func mockHandler(spec toolSpec) sdk.ToolHandler {
	return func(args map[string]any) (any, error) {
		if spec.MockTemplate != "" {
			return renderMockTemplate(spec, args)
		}
		if spec.MockResult != nil {
			return spec.MockResult, nil
		}
		return nil, fmt.Errorf(
			"tool %q is mode %q but declares neither mock_result nor mock_template",
			spec.Name, toolModeMock)
	}
}

// renderMockTemplate renders a mock_template against the call arguments.
//
// The rendering and result handling deliberately mirror PromptKit's own
// MockScriptedExecutor: missing keys render as the zero value, and the rendered
// text is parsed back as JSON, falling back to {"result": <text>} when it is not
// valid JSON. A deployed agent that answered differently from `promptarena run`
// would make local evals meaningless.
func renderMockTemplate(spec toolSpec, args map[string]any) (any, error) {
	tmpl, err := template.New(spec.Name).Option("missingkey=zero").Parse(spec.MockTemplate)
	if err != nil {
		return nil, fmt.Errorf("tool %q: parse mock_template: %w", spec.Name, err)
	}

	var out strings.Builder
	if execErr := tmpl.Execute(&out, args); execErr != nil {
		return nil, fmt.Errorf("tool %q: render mock_template: %w", spec.Name, execErr)
	}

	var parsed any
	if json.Unmarshal([]byte(out.String()), &parsed) != nil {
		return map[string]any{"result": out.String()}, nil
	}
	return parsed, nil
}

// registerToolExecutors attaches an executor to the conversation for every tool
// whose mode this runtime supports, returning the tools it could not wire.
//
// Unsupported modes are reported rather than skipped silently: a tool that looks
// configured but never runs is the failure this whole path exists to prevent.
func registerToolExecutors(conv *sdk.Conversation, specs map[string]toolSpec) []string {
	var unsupported []string

	for name := range specs {
		spec := specs[name]
		if spec.Name == "" {
			spec.Name = name
		}

		switch spec.Mode {
		case toolModeMock:
			conv.OnTool(name, mockHandler(spec))
		case toolModeLive:
			if spec.HTTP == nil || spec.HTTP.URL == "" {
				unsupported = append(unsupported,
					fmt.Sprintf("%s (mode live without an http url)", name))
				continue
			}
			conv.OnToolHTTP(name, httpToolConfig(spec))
		default:
			unsupported = append(unsupported,
				fmt.Sprintf("%s (mode %q)", name, spec.Mode))
		}
	}

	return unsupported
}

// httpToolConfig builds the SDK's HTTP tool config from a spec.
func httpToolConfig(spec toolSpec) *sdktools.HTTPToolConfig {
	opts := []sdktools.HTTPToolOption{}
	if spec.HTTP.Method != "" {
		opts = append(opts, sdktools.WithMethod(spec.HTTP.Method))
	}
	return sdktools.NewHTTPToolConfig(spec.HTTP.URL, opts...)
}
