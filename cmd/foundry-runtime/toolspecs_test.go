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

	if httpToolConfig(spec) == nil {
		t.Fatal("httpToolConfig = nil, want a config")
	}
}

// A method-less spec still needs a usable config: the SDK's own default
// applies rather than the tool silently not running.
func TestHTTPToolConfigWithoutAMethod(t *testing.T) {
	spec := toolSpec{Name: "fetch", HTTP: &toolHTTP{URL: "https://example.test/x"}}

	if httpToolConfig(spec) == nil {
		t.Fatal("httpToolConfig = nil, want a config")
	}
}

// mock_template wins over mock_result, matching the arena's own precedence —
// a deployed agent that resolved them differently would make local evals
// meaningless.
func TestMockHandlerTemplateWinsOverResult(t *testing.T) {
	spec := toolSpec{
		Name:         "greet",
		MockResult:   map[string]any{"from": "result"},
		MockTemplate: `{"from":"template"}`,
	}

	got, err := mockHandler(spec)(map[string]any{})
	if err != nil {
		t.Fatalf("mockHandler: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "template") {
		t.Errorf("handler returned %s, want the template to win", encoded)
	}
}

func TestMockHandlerReturnsTheResult(t *testing.T) {
	spec := toolSpec{Name: "greet", MockResult: map[string]any{"ok": true}}

	got, err := mockHandler(spec)(map[string]any{})
	if err != nil {
		t.Fatalf("mockHandler: %v", err)
	}
	if got == nil {
		t.Error("handler returned nil, want the configured result")
	}
}

// A tool declared as a mock with nothing to return is a configuration mistake
// worth reporting at call time rather than answering null.
func TestMockHandlerWithoutAnyMock(t *testing.T) {
	spec := toolSpec{Name: "empty"}

	if _, err := mockHandler(spec)(map[string]any{}); err == nil {
		t.Fatal("mockHandler succeeded with neither a result nor a template")
	}
}

// Rendered output that is not JSON still has to reach the model as something,
// mirroring PromptKit's own executor.
func TestRenderMockTemplateNonJSONFallsBack(t *testing.T) {
	spec := toolSpec{Name: "plain", MockTemplate: `just text`}

	got, err := renderMockTemplate(spec, map[string]any{})
	if err != nil {
		t.Fatalf("renderMockTemplate: %v", err)
	}
	if got == nil {
		t.Error("rendered nil, want the text wrapped")
	}
}

// Every binding the arena can declare has to survive the trip from injected
// JSON to the SDK config. The adapter forwards a tool spec verbatim, so a field
// this runtime has no home for is dropped by the decoder in silence: the tool
// deploys, reports healthy, and calls its API without the header it was told to
// send. Asserting the config is non-nil — which is all the older tests did —
// cannot see that, so this walks the whole path and checks each field.
func TestHTTPToolConfigCarriesEveryBinding(t *testing.T) {
	raw := `{"lookup":{
		"name":"lookup",
		"mode":"live",
		"http":{
			"url":"https://example.test/x",
			"method":"POST",
			"headers":{"X-Api-Key":"secret","X-Trace":"on"},
			"headers_from_env":["Authorization=LOOKUP_TOKEN"],
			"timeout_ms":2500,
			"redact":["ssn","card"]
		}
	}}`

	specs, err := parseToolSpecs(raw)
	if err != nil {
		t.Fatalf("parseToolSpecs: %v", err)
	}

	got := httpToolConfig(specs["lookup"]).ToDescriptorConfig()

	if got.URL != "https://example.test/x" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Method != "POST" {
		t.Errorf("Method = %q", got.Method)
	}
	if got.Headers["X-Api-Key"] != "secret" || got.Headers["X-Trace"] != "on" {
		t.Errorf("Headers = %v, want both static headers", got.Headers)
	}
	if len(got.HeadersFromEnv) != 1 || got.HeadersFromEnv[0] != "Authorization=LOOKUP_TOKEN" {
		t.Errorf("HeadersFromEnv = %v", got.HeadersFromEnv)
	}
	if got.TimeoutMs != 2500 {
		t.Errorf("TimeoutMs = %d, want 2500", got.TimeoutMs)
	}
	if len(got.Redact) != 2 || got.Redact[0] != "ssn" || got.Redact[1] != "card" {
		t.Errorf("Redact = %v, want [ssn card]", got.Redact)
	}
}

// Header order must not vary between builds of the same spec: map iteration is
// random, and a config that differs run to run makes the deploy hash unstable.
func TestHTTPToolConfigOrdersHeadersStably(t *testing.T) {
	spec := toolSpec{Name: "fetch", HTTP: &toolHTTP{
		URL:     "https://example.test/x",
		Headers: map[string]string{"B": "2", "A": "1", "C": "3"},
	}}

	first := httpToolConfig(spec).ToDescriptorConfig().Headers
	for i := 0; i < 20; i++ {
		if got := httpToolConfig(spec).ToDescriptorConfig().Headers; len(got) != len(first) {
			t.Fatalf("header count varied: %v vs %v", got, first)
		}
	}
	if first["A"] != "1" || first["B"] != "2" || first["C"] != "3" {
		t.Errorf("Headers = %v", first)
	}
}
