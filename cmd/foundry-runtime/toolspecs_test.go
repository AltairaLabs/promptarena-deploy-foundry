package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolSpecsEmpty(t *testing.T) {
	got, err := parseToolSpecs("")
	if err != nil {
		t.Fatalf("parseToolSpecs: %v", err)
	}
	if got != nil {
		t.Errorf("parseToolSpecs(\"\") = %v, want nil", got)
	}
}

func TestParseToolSpecsRejectsMalformedJSON(t *testing.T) {
	if _, err := parseToolSpecs(`{`); err == nil {
		t.Fatal("parseToolSpecs accepted malformed JSON")
	}
}

// The compiled pack carries only tool schemas, so a tool whose execution
// config never arrives is one the model can call but nothing can fulfil.
func TestParseToolSpecsKeepsExecutionConfig(t *testing.T) {
	raw := `{"lookup":{"name":"lookup","mode":"mock","mock_result":{"answer":42}}}`

	got, err := parseToolSpecs(raw)
	if err != nil {
		t.Fatalf("parseToolSpecs: %v", err)
	}
	spec, ok := got["lookup"]
	if !ok {
		t.Fatalf("specs = %v, want a lookup entry", got)
	}
	if spec.Mode != "mock" {
		t.Errorf("Mode = %q, want mock", spec.Mode)
	}
	if spec.MockResult == nil {
		t.Error("MockResult is nil, want the configured value")
	}
}

func TestRenderMockTemplate(t *testing.T) {
	spec := toolSpec{Name: "greet", MockTemplate: `{"hello":"{{.who}}"}`}

	got, err := renderMockTemplate(spec, map[string]any{"who": "world"})
	if err != nil {
		t.Fatalf("renderMockTemplate: %v", err)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "world") {
		t.Errorf("rendered %s, want the argument substituted", encoded)
	}
}

func TestRenderMockTemplateRejectsABadTemplate(t *testing.T) {
	spec := toolSpec{Name: "broken", MockTemplate: `{{ .unclosed `}

	if _, err := renderMockTemplate(spec, nil); err == nil {
		t.Fatal("renderMockTemplate accepted an unparseable template")
	}
}

// A missing argument renders as a zero value rather than failing the turn:
// one absent field should not take the whole tool call down.
func TestRenderMockTemplateMissingArgument(t *testing.T) {
	spec := toolSpec{Name: "greet", MockTemplate: `{"hello":"{{.absent}}"}`}

	if _, err := renderMockTemplate(spec, map[string]any{}); err != nil {
		t.Errorf("renderMockTemplate: %v", err)
	}
}

// httpToolConfig assumes a non-nil HTTP block; registerToolExecutors is the
// guard that guarantees it.
func TestHTTPToolConfig(t *testing.T) {
	spec := toolSpec{
		Name: "fetch",
		HTTP: &toolHTTP{Method: "POST", URL: "https://example.test/x"},
	}

	if got := httpToolConfig(spec); got == nil {
		t.Fatal("httpToolConfig = nil, want a config")
	}
}

// A method-less spec still needs a usable config: the SDK's own default
// applies rather than the tool silently not running.
func TestHTTPToolConfigWithoutAMethod(t *testing.T) {
	spec := toolSpec{Name: "fetch", HTTP: &toolHTTP{URL: "https://example.test/x"}}

	if got := httpToolConfig(spec); got == nil {
		t.Fatal("httpToolConfig = nil, want a config")
	}
}
