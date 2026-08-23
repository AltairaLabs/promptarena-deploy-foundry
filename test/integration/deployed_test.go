//go:build integration

// Package integration holds tests that deploy to a real Azure AI Foundry
// project.
//
// They are excluded from normal builds by the integration build tag and skip
// unless FOUNDRY_TEST_ACCOUNT, FOUNDRY_TEST_PROJECT and FOUNDRY_TEST_IMAGE are
// set. Running them creates billable Foundry agents; each test deletes what it
// created, including on failure.
//
// These are the only tests that prove the adapter works against Azure rather
// than against our own fakes. Everything else in this repo talks to a
// simulated client.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/AltairaLabs/promptarena/deploy"

	"github.com/AltairaLabs/promptarena-deploy-foundry/internal/foundry"
)

const (
	envAccount = "FOUNDRY_TEST_ACCOUNT"
	envProject = "FOUNDRY_TEST_PROJECT"
	envImage   = "FOUNDRY_TEST_IMAGE"
	envModel   = "FOUNDRY_TEST_MODEL"
)

// defaultModel is the Azure OpenAI deployment name the pack binds to. This is
// a deployment name, not a model name, and it must already exist in the target
// project — the adapter does not create one. Override with FOUNDRY_TEST_MODEL.
const defaultModel = "gpt-4-1-mini"

// authScope is the Entra scope for the Foundry data plane, matching the one the
// adapter's own client uses.
const authScope = "https://ai.azure.com/.default"

// invokeTimeout is generous: the first call to a fresh agent cold-starts a
// session sandbox, which takes several seconds.
const invokeTimeout = 300 * time.Second

// applyTimeout bounds a full deploy. Version creation polls until the version
// leaves the creating state.
const applyTimeout = 20 * time.Minute

// featurePack exercises a system prompt, a tool and a validator together, so a
// passing run says more than "the container started".
const featurePack = `{
  "$schema": "https://promptpack.org/schema/latest/promptpack.schema.json",
  "id": "foundry-integration",
  "name": "Foundry Integration Pack",
  "version": "1.0.0",
  "template_engine": { "version": "v1", "syntax": "{{variable}}" },
  "prompts": {
    "main": {
      "id": "main",
      "name": "Support Agent",
      "version": "1.0.0",
      "system_template": "You are a terse support agent. Answer in one short sentence.",
      "tools": ["lookup_order"],
      "validators": [
        { "type": "length", "enabled": true, "params": { "max_characters": 4000 } }
      ]
    }
  },
  "tools": {
    "lookup_order": {
      "name": "lookup_order",
      "description": "Look up an order by its id",
      "parameters": {
        "type": "object",
        "properties": { "order_id": { "type": "string", "description": "The order id" } },
        "required": ["order_id"]
      }
    }
  }
}`

// mockOrderStatus is the value only the tool knows. The model cannot produce it
// from the prompt, so seeing it in an answer proves the runtime actually ran
// the tool rather than improvising an order status.
const mockOrderStatus = "delivered to a purple locker in Reykjavik"

// featureArena is the arena config the CLI would hand the adapter. The compiled
// pack carries only the tool's schema; its execution config lives here.
const featureArena = `{
  "tool_specs": {
    "lookup_order": {
      "name": "lookup_order",
      "mode": "mock",
      "mock_template": "{\"order_id\":\"{{.order_id}}\",\"status\":\"` +
	mockOrderStatus + `\"}"
    }
  }
}`

// testEnv holds the resolved configuration for a run.
type testEnv struct {
	Account string
	Project string
	Image   string
	Model   string
}

// requireEnv skips the test unless the required variables are present, so a
// plain `go test ./...` can never create billable resources.
func requireEnv(t *testing.T) testEnv {
	t.Helper()

	account := os.Getenv(envAccount)
	project := os.Getenv(envProject)
	image := os.Getenv(envImage)
	if account == "" || project == "" || image == "" {
		t.Skipf("set %s, %s and %s to run deployed integration tests",
			envAccount, envProject, envImage)
	}

	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	return testEnv{Account: account, Project: project, Image: image, Model: model}
}

// deployConfig builds the adapter's deploy config JSON.
func deployConfig(t *testing.T, env testEnv) string {
	t.Helper()

	cfg := map[string]any{
		"account": env.Account,
		"project": env.Project,
		"image":   env.Image,
		// Foundry rejects a create without these — see the adapter fix that
		// this suite's first real run prompted. Kept explicit here so the
		// tests exercise a config Azure will actually accept.
		"cpu":       "1",
		"memory":    "2Gi",
		"protocols": []string{foundry.ProtocolInvocations},
		"providers": []map[string]any{
			// type is the provider FAMILY, not the platform: "azure" fails at
			// container startup with "unsupported provider type: azure".
			// model is the Azure deployment name, not the model name.
			{"name": "default", "role": "llm", "type": "openai", "model": env.Model},
		},
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal deploy config: %v", err)
	}
	return string(encoded)
}

// stateShape is the subset of adapter state these tests read.
//
// Tests that re-apply must pass the ORIGINAL state string as PriorState, never
// a re-encode of this struct: it omits pack_hash and config_hash, and without
// them the adapter sees a changed pack and rolls a version.
type stateShape struct {
	AgentName     string   `json:"agent_name"`
	ServedVersion string   `json:"served_version"`
	PriorVersions []string `json:"prior_versions"`
}

// parseState pulls the fields these tests assert on out of the state blob.
func parseState(t *testing.T, state string) stateShape {
	t.Helper()

	var parsed stateShape
	if err := json.Unmarshal([]byte(state), &parsed); err != nil {
		t.Fatalf("parse adapter state: %v", err)
	}
	return parsed
}

// applyPack deploys packJSON and returns the resulting state, registering
// cleanup that destroys the agent even when the test fails.
func applyPack(t *testing.T, env testEnv, packJSON string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	cfgJSON := deployConfig(t, env)
	provider := foundry.NewProvider()
	state, err := provider.Apply(ctx, &deploy.PlanRequest{
		PackJSON:     packJSON,
		DeployConfig: cfgJSON,
		ArenaConfig:  featureArena,
	}, nil)
	if err != nil {
		// State is returned even on partial failure, so clean up what landed.
		if state != "" {
			destroyQuietly(t, cfgJSON, state)
		}
		t.Fatalf("Apply: %v", err)
	}

	t.Cleanup(func() { destroyQuietly(t, cfgJSON, state) })
	return state
}

// destroyQuietly tears down whatever the state records, reporting failures
// loudly enough that a leaked billable agent is not missed.
func destroyQuietly(t *testing.T, cfgJSON, state string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	provider := foundry.NewProvider()
	if err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON,
		PriorState:   state,
	}, nil); err != nil {
		t.Errorf("cleanup: destroy failed (%v) — CHECK THE FOUNDRY PORTAL FOR A LEAKED AGENT", err)
	}
}

// invokeURL builds the endpoint the deployed agent serves on.
func invokeURL(env testEnv, agent, session string) string {
	url := fmt.Sprintf(
		"https://%s.services.ai.azure.com/api/projects/%s/agents/%s"+
			"/endpoint/protocols/invocations?api-version=v1",
		env.Account, env.Project, agent)
	if session != "" {
		// The invocations endpoint reads the session id from the query string
		// only; a body field or header is forwarded but does not bind it.
		url += "&agent_session_id=" + session
	}
	return url
}

// authedPost issues an Entra-authorized POST against the deployed agent.
func authedPost(t *testing.T, url, body string, sse bool) (int, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), invokeTimeout)
	defer cancel()

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		t.Fatalf("azure credential: %v", err)
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{authScope}})
	if err != nil {
		t.Fatalf("acquire token: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	req.Header.Set("Content-Type", "application/json")
	if sse {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var sb strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteString("\n")
	}
	return resp.StatusCode, sb.String()
}

// ask sends one message to the deployed agent and returns the output text.
func ask(t *testing.T, env testEnv, agent, session, message string) string {
	t.Helper()

	body, err := json.Marshal(map[string]any{"message": message, "stream": false})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	status, raw := authedPost(t, invokeURL(env, agent, session), string(body), false)
	if status != http.StatusOK {
		t.Fatalf("invoke returned %d: %s", status, raw)
	}

	var out struct {
		Output         string `json:"output"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		t.Fatalf("parse invocation response %q: %v", raw, err)
	}
	if out.Output == "" {
		t.Fatalf("agent returned no output: %s", raw)
	}
	return out.Output
}

// --- Deploy lifecycle -------------------------------------------------------

// TestDeployed_ApplyCreatesAgentAndServesAVersion is the base case: a deploy
// must leave an agent, a version, and an endpoint pointed at that version.
// Everything else here builds on it.
func TestDeployed_ApplyCreatesAgentAndServesAVersion(t *testing.T) {
	env := requireEnv(t)

	state := parseState(t, applyPack(t, env, featurePack))

	if state.AgentName == "" {
		t.Fatal("state records no agent name after a successful apply")
	}
	if state.ServedVersion == "" {
		t.Error("state records no served version; the endpoint points at nothing")
	}
}

// TestDeployed_StatusReportsDeployed checks the adapter's own view agrees with
// Azure's after a deploy.
func TestDeployed_StatusReportsDeployed(t *testing.T) {
	env := requireEnv(t)
	state := applyPack(t, env, featurePack)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	resp, err := foundry.NewProvider().Status(ctx, &deploy.StatusRequest{
		DeployConfig: deployConfig(t, env),
		PriorState:   state,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Status != foundry.StatusDeployed {
		t.Errorf("Status = %q, want %q (resources: %+v)",
			resp.Status, foundry.StatusDeployed, resp.Resources)
	}
}

// --- Invocation -------------------------------------------------------------

// TestDeployed_UnaryInvocation proves the deployed container serves the
// invocations protocol and the pack's system prompt reached the model.
func TestDeployed_UnaryInvocation(t *testing.T) {
	env := requireEnv(t)
	state := parseState(t, applyPack(t, env, featurePack))

	answer := ask(t, env, state.AgentName, "", "Say the word acknowledged and nothing else.")

	if !strings.Contains(strings.ToLower(answer), "acknowledged") {
		t.Errorf("answer %q does not contain the requested word", answer)
	}
}

// TestDeployed_ToolCalling asks a real model to call the pack's tool. The tool
// returns a value the model could not otherwise know, so finding it in the
// answer proves the whole path ran: model -> tool call -> arena mock -> model.
func TestDeployed_ToolCalling(t *testing.T) {
	env := requireEnv(t)
	state := parseState(t, applyPack(t, env, featurePack))

	answer := ask(t, env, state.AgentName, "",
		"What is the status of order A-4471? Use the lookup_order tool and quote the status verbatim.")

	if !strings.Contains(strings.ToLower(answer), "purple locker") {
		t.Errorf("answer %q does not carry the tool's value (%q); the tool likely never ran",
			answer, mockOrderStatus)
	}
}

// TestDeployed_StreamingInvocation checks the SSE path terminates properly.
// A stream that never sends its done frame hangs every client that reads to
// completion, which unit tests against a fake writer do not catch.
func TestDeployed_StreamingInvocation(t *testing.T) {
	env := requireEnv(t)
	state := parseState(t, applyPack(t, env, featurePack))

	body, err := json.Marshal(map[string]any{
		"message": "Count to three.", "stream": true,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	status, raw := authedPost(t, invokeURL(env, state.AgentName, ""), string(body), true)
	if status != http.StatusOK {
		t.Fatalf("stream returned %d: %s", status, raw)
	}
	if !strings.Contains(raw, "data:") {
		t.Errorf("no SSE frames in response: %s", raw)
	}
	if !strings.Contains(raw, "[DONE]") {
		t.Errorf("stream did not terminate with a done frame; clients reading to "+
			"completion would hang. Got: %s", raw)
	}
}

// TestDeployed_SessionCarriesConversation pins what the session id actually
// does. The endpoint binds a session from the query string only, so this is
// the behaviour a body field would silently fail to provide.
func TestDeployed_SessionCarriesConversation(t *testing.T) {
	env := requireEnv(t)
	state := parseState(t, applyPack(t, env, featurePack))

	session := fmt.Sprintf("itest-%d", time.Now().UnixNano())

	ask(t, env, state.AgentName, session, "Remember the code word: flamingo.")
	answer := ask(t, env, state.AgentName, session, "What was the code word?")

	if !strings.Contains(strings.ToLower(answer), "flamingo") {
		t.Errorf("second turn %q did not recall the first; the session did not bind", answer)
	}
}

// --- Re-apply and versioning ------------------------------------------------

// TestDeployed_ReapplyIsIdempotent re-applies the same pack and config. Foundry
// versions are immutable, so an unchanged deploy must not churn one.
func TestDeployed_ReapplyIsIdempotent(t *testing.T) {
	env := requireEnv(t)
	firstRaw := applyPack(t, env, featurePack)
	first := parseState(t, firstRaw)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	second, err := foundry.NewProvider().Apply(ctx, &deploy.PlanRequest{
		PackJSON:     featurePack,
		DeployConfig: deployConfig(t, env),
		ArenaConfig:  featureArena,
		PriorState:   firstRaw,
	}, nil)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	after := parseState(t, second)
	if after.AgentName != first.AgentName {
		t.Errorf("agent name changed on re-apply: %q -> %q", first.AgentName, after.AgentName)
	}
	if after.ServedVersion != first.ServedVersion {
		t.Errorf("served version churned on an unchanged deploy: %q -> %q",
			first.ServedVersion, after.ServedVersion)
	}
}

// TestDeployed_ChangedPackRollsTheServedVersion checks the other half: a pack
// that actually changed must produce a new version and repoint the endpoint,
// keeping the old one in prior_versions.
func TestDeployed_ChangedPackRollsTheServedVersion(t *testing.T) {
	env := requireEnv(t)
	firstRaw := applyPack(t, env, featurePack)
	first := parseState(t, firstRaw)

	changed := strings.Replace(featurePack,
		"You are a terse support agent.",
		"You are a brief support agent.", 1)
	if changed == featurePack {
		t.Fatal("test fixture did not change; the assertion below would be vacuous")
	}

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	second, err := foundry.NewProvider().Apply(ctx, &deploy.PlanRequest{
		PackJSON:     changed,
		DeployConfig: deployConfig(t, env),
		ArenaConfig:  featureArena,
		PriorState:   firstRaw,
	}, nil)
	if err != nil {
		t.Fatalf("Apply of changed pack: %v", err)
	}

	after := parseState(t, second)
	if after.ServedVersion == first.ServedVersion {
		t.Errorf("served version did not roll for a changed pack: still %q", after.ServedVersion)
	}
	if len(after.PriorVersions) == 0 {
		t.Error("the superseded version was not recorded in prior_versions")
	}
}

// --- Drift ------------------------------------------------------------------

// TestDeployed_DriftIsDetectedWhenTheAgentIsDeleted deletes the agent behind
// the adapter's back and checks Plan notices. This is the case the shared
// drift contract exists for, and the only place it is proven against a real
// control plane rather than a fake that answers however we told it to.
func TestDeployed_DriftIsDetectedWhenTheAgentIsDeleted(t *testing.T) {
	env := requireEnv(t)
	cfgJSON := deployConfig(t, env)
	state := applyPack(t, env, featurePack)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	// Destroy out of band, then plan against the now-stale state.
	if err := foundry.NewProvider().Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON,
		PriorState:   state,
	}, nil); err != nil {
		t.Fatalf("out-of-band destroy: %v", err)
	}

	plan, err := foundry.NewProvider().Plan(ctx, &deploy.PlanRequest{
		PackJSON:     featurePack,
		DeployConfig: cfgJSON,
		ArenaConfig:  featureArena,
		PriorState:   state,
	})
	if err != nil {
		t.Fatalf("Plan after out-of-band delete: %v", err)
	}

	var sawDrift, sawCreate bool
	for _, c := range plan.Changes {
		switch c.Action {
		case deploy.ActionDrift:
			sawDrift = true
		case deploy.ActionCreate:
			sawCreate = true
		}
	}
	if !sawDrift {
		t.Errorf("Plan did not report drift for a deleted agent: %+v", plan.Changes)
	}
	if !sawCreate {
		t.Errorf("Plan did not fall back to creating the agent: %+v", plan.Changes)
	}
	if !strings.Contains(plan.Summary, "drifted") {
		t.Errorf("Summary = %q, want it to mention drift", plan.Summary)
	}
}

// --- Destroy ----------------------------------------------------------------

// TestDeployed_DestroyIsIdempotent checks destroy converges. A second destroy
// against an already-clean project must not fail, or every retried teardown
// turns into a manual cleanup.
func TestDeployed_DestroyIsIdempotent(t *testing.T) {
	env := requireEnv(t)
	cfgJSON := deployConfig(t, env)
	state := applyPack(t, env, featurePack)

	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	provider := foundry.NewProvider()
	if err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON, PriorState: state,
	}, nil); err != nil {
		t.Fatalf("first destroy: %v", err)
	}
	if err := provider.Destroy(ctx, &deploy.DestroyRequest{
		DeployConfig: cfgJSON, PriorState: state,
	}, nil); err != nil {
		t.Errorf("destroying an already-destroyed deploy must be a no-op, got: %v", err)
	}
}
