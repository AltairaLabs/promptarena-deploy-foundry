package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// fakeCredential hands out a static token so the pipeline's bearer policy runs
// without contacting Entra.
type fakeCredential struct{}

func (fakeCredential) GetToken(
	_ context.Context, _ policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "fake-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// testClient wires a restClient to an httptest server.
func testClient(t *testing.T, handler http.HandlerFunc) *restClient {
	t.Helper()
	// TLS, not plain HTTP: azcore's bearer-token policy refuses to attach a
	// credential to a non-https endpoint, so a plain httptest server would fail
	// every request before the handler ran.
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	c, err := newRESTClient(srv.URL, fakeCredential{}, srv.Client())
	if err != nil {
		t.Fatalf("newRESTClient: %v", err)
	}
	// Tests must not wait on real backoff.
	c.pollDelay = time.Millisecond
	return c
}

func TestRESTGetAgent(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		writeJSON(t, w, http.StatusOK, map[string]any{
			"name": "support-pack",
			"agent_endpoint": map[string]any{
				"version_selector": map[string]any{
					"version_selection_rules": []any{
						map[string]any{"version": "3", "traffic_percentage": 100},
					},
				},
			},
		})
	})

	agent, err := c.GetAgent(context.Background(), "support-pack")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	if gotPath != "/agents/support-pack" {
		t.Errorf("path = %q, want /agents/support-pack", gotPath)
	}
	if !strings.Contains(gotQuery, "api-version="+apiVersion) {
		t.Errorf("query = %q, want it to carry api-version=%s", gotQuery, apiVersion)
	}
	if gotAuth != "Bearer fake-token" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if agent.ServedVersion != "3" {
		t.Errorf("ServedVersion = %q, want 3", agent.ServedVersion)
	}
}

func TestRESTGetAgentNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.GetAgent(context.Background(), "nope")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want ErrAgentNotFound", err)
	}
}

// A non-404 failure must not be mistaken for "does not exist" — that would
// turn an auth or throttling error into a spurious create.
func TestRESTGetAgentServerErrorIsNotNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := c.GetAgent(context.Background(), "a")
	if err == nil {
		t.Fatal("GetAgent succeeded on a 403")
	}
	if errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want a 403 not to read as not-found", err)
	}
}

func TestRESTCreateAgentSendsHostedDefinition(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		decodeBody(t, r, &body)
		writeJSON(t, w, http.StatusCreated, map[string]any{"name": "support-pack"})
	})

	spec := &AgentSpec{
		Name: "support-pack", Image: "acr.azurecr.io/x:1",
		CPU: "1", Memory: "2Gi",
		Protocols:          []string{ProtocolInvocations, ProtocolResponses},
		IdleTimeoutMinutes: 20,
		Env:                map[string]string{"PROMPTPACK_PACK": "{}"},
		Metadata:           map[string]string{"team": "platform"},
	}
	if _, err := c.CreateAgent(context.Background(), spec); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if body["name"] != "support-pack" {
		t.Errorf("name = %v, want support-pack", body["name"])
	}

	// Verified against a live project: the agents API stores key/value data
	// under "metadata" and silently discards a "tags" field, so sending tags
	// loses the managed attribution without any error.
	meta, ok := body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("body has no metadata object: %v", body)
	}
	if meta["team"] != "platform" {
		t.Errorf("metadata.team = %v, want platform", meta["team"])
	}
	if _, present := body["tags"]; present {
		t.Errorf("body sent a tags field, which the API ignores: %v", body["tags"])
	}
	def, ok := body["definition"].(map[string]any)
	if !ok {
		t.Fatalf("body has no definition object: %v", body)
	}
	if def["kind"] != hostedAgentKind {
		t.Errorf("definition.kind = %v, want %q", def["kind"], hostedAgentKind)
	}
	if def["cpu"] != "1" || def["memory"] != "2Gi" {
		t.Errorf("cpu/memory = %v/%v, want 1/2Gi", def["cpu"], def["memory"])
	}

	container, ok := def["container_configuration"].(map[string]any)
	if !ok || container["image"] != "acr.azurecr.io/x:1" {
		t.Errorf("container_configuration = %v, want the image", def["container_configuration"])
	}

	protocols, ok := def["protocol_versions"].([]any)
	if !ok || len(protocols) != 2 {
		t.Fatalf("protocol_versions = %v, want two entries", def["protocol_versions"])
	}
	first, _ := protocols[0].(map[string]any)
	if first["protocol"] != ProtocolInvocations || first["version"] != protocolVersion {
		t.Errorf("protocol_versions[0] = %v, want invocations at %s", first, protocolVersion)
	}
}

// Zero means "unset": sending 0 would be rejected, since the platform's floor
// is 5 minutes.
func TestRESTCreateAgentOmitsUnsetIdleTimeout(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		decodeBody(t, r, &body)
		writeJSON(t, w, http.StatusCreated, map[string]any{"name": "a"})
	})

	spec := &AgentSpec{Name: "a", Image: "i", Protocols: []string{ProtocolInvocations}}
	if _, err := c.CreateAgent(context.Background(), spec); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	def, _ := body["definition"].(map[string]any)
	if _, present := def["idle_timeout_minutes"]; present {
		t.Errorf("idle_timeout_minutes was sent as %v, want it omitted", def["idle_timeout_minutes"])
	}
}

// Create returns before the version is serving, so CreateVersion polls until
// the status settles rather than reporting a version that is not yet running.
func TestRESTCreateVersionPollsUntilActive(t *testing.T) {
	polls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(t, w, http.StatusCreated,
				map[string]any{"version": "4", "status": VersionStatusCreating})
			return
		}
		polls++
		status := VersionStatusCreating
		if polls >= 2 {
			status = VersionStatusActive
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"version": "4", "status": status})
	})

	version, err := c.CreateVersion(context.Background(), "a", &AgentSpec{Name: "a", Image: "i"})
	if err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	if version.Status != VersionStatusActive {
		t.Errorf("Status = %q, want active", version.Status)
	}
	if polls < 2 {
		t.Errorf("polled %d times, want it to keep polling while creating", polls)
	}
}

// The most likely failure is an ACR pull denial, where the create succeeds and
// the container then cannot start. The reason has to reach the user.
func TestRESTCreateVersionSurfacesFailureReason(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(t, w, http.StatusCreated,
				map[string]any{"version": "4", "status": VersionStatusCreating})
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"version": "4", "status": VersionStatusFailed,
			"error": map[string]any{"message": "failed to pull image: unauthorized"},
		})
	})

	_, err := c.CreateVersion(context.Background(), "a", &AgentSpec{Name: "a", Image: "i"})
	if !errors.Is(err, ErrVersionFailed) {
		t.Fatalf("err = %v, want ErrVersionFailed", err)
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("err = %v, want it to carry the platform's reason", err)
	}
}

// The endpoint's protocol list is separate from the version's, and defaults to
// ["responses"]. Verified against a live project: an agent whose version
// declares only invocations still gets an endpoint exposing responses, which
// the container does not serve — so the endpoint is unreachable unless the
// adapter sets this too.
func TestRESTSetEndpointSendsProtocols(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		decodeBody(t, r, &body)
		writeJSON(t, w, http.StatusOK, map[string]any{"name": "a"})
	})

	err := c.SetEndpoint(context.Background(), "a", "7",
		[]string{ProtocolInvocations, ProtocolResponses})
	if err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}

	endpoint, ok := body["agent_endpoint"].(map[string]any)
	if !ok {
		t.Fatalf("body has no agent_endpoint: %v", body)
	}
	protocols, ok := endpoint["protocols"].([]any)
	if !ok {
		t.Fatalf("agent_endpoint has no protocols: %v", endpoint)
	}
	if len(protocols) != 2 || protocols[0] != ProtocolInvocations {
		t.Errorf("protocols = %v, want the configured list", protocols)
	}
}

// Served version and protocols are one concern, so they travel in one patch —
// two calls would leave a window where the endpoint serves the new version
// over the wrong protocol.
func TestRESTSetEndpointSendsMergePatch(t *testing.T) {
	var body map[string]any
	var gotMethod, gotContentType string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotContentType = r.Method, r.Header.Get("Content-Type")
		decodeBody(t, r, &body)
		writeJSON(t, w, http.StatusOK, map[string]any{"name": "a"})
	})

	if err := c.SetEndpoint(context.Background(), "a", "7", []string{ProtocolInvocations}); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if gotContentType != mergePatchContentType {
		t.Errorf("Content-Type = %q, want %q", gotContentType, mergePatchContentType)
	}

	rules := selectionRules(t, body)
	if len(rules) != 1 {
		t.Fatalf("version_selection_rules = %v, want exactly one", rules)
	}
	rule, _ := rules[0].(map[string]any)
	if rule["version"] != "7" {
		t.Errorf("rule version = %v, want 7", rule["version"])
	}
	// Traffic splitting is not supported; the single rule must be 100%.
	if pct, _ := rule["traffic_percentage"].(float64); pct != 100 {
		t.Errorf("traffic_percentage = %v, want 100", rule["traffic_percentage"])
	}
	if rule["kind"] != fixedRatioRuleKind {
		t.Errorf("rule kind = %v, want %q", rule["kind"], fixedRatioRuleKind)
	}
}

// Destroy must converge on "gone" rather than fail on an already-clean project.
func TestRESTDeleteAgentTreats404AsSuccess(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if err := c.DeleteAgent(context.Background(), "gone"); err != nil {
		t.Errorf("DeleteAgent = %v, want nil on a 404", err)
	}
}

func TestRESTDeleteAgentPropagatesOtherErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	if err := c.DeleteAgent(context.Background(), "a"); err == nil {
		t.Error("DeleteAgent succeeded on a 403")
	}
}

func TestRESTListAgents(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Envelope verified against a live project: the list API is
		// OpenAI-shaped ({"data":[...],"has_more":...,"object":"list"}), not
		// ARM-shaped ({"value":[...]}).
		writeJSON(t, w, http.StatusOK, map[string]any{
			"object":   "list",
			"has_more": false,
			"data": []any{
				map[string]any{"name": "a"},
				map[string]any{"name": "b"},
			},
		})
	})

	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 || agents[0].Name != "a" {
		t.Errorf("ListAgents() = %v, want two agents", agents)
	}
}

func TestRESTGetVersionNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.GetVersion(context.Background(), "a", "1")
	if !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("err = %v, want ErrVersionNotFound", err)
	}
}

func TestRESTClientSatisfiesInterface(t *testing.T) {
	var _ foundryClient = (*restClient)(nil)
}

// An error body must reach the user; a bare status code is not actionable.
func TestRESTErrorIncludesResponseBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "cpu must be one of 0.5, 1, 2"},
		})
	})

	_, err := c.CreateAgent(context.Background(), &AgentSpec{Name: "a", Image: "i"})
	if err == nil {
		t.Fatal("CreateAgent succeeded on a 400")
	}
	if !strings.Contains(err.Error(), "cpu must be one of") {
		t.Errorf("err = %v, want it to carry the API's message", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func decodeBody(t *testing.T, r *http.Request, into any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}

// selectionRules digs the version selection rules out of a merge-patch body.
func selectionRules(t *testing.T, body map[string]any) []any {
	t.Helper()
	endpoint, ok := body["agent_endpoint"].(map[string]any)
	if !ok {
		t.Fatalf("body has no agent_endpoint: %v", body)
	}
	selector, ok := endpoint["version_selector"].(map[string]any)
	if !ok {
		t.Fatalf("body has no version_selector: %v", endpoint)
	}
	rules, ok := selector["version_selection_rules"].([]any)
	if !ok {
		t.Fatalf("body has no version_selection_rules: %v", selector)
	}
	return rules
}

// Foundry answers 404 for both "no such agent" and "no such project", so a
// typo in project would otherwise read as agent drift and produce a confident
// plan that can never apply. Verified against a real account: the body is
// {"error":{"code":"ResourceNotFound","message":"The project does not exist."}}
func TestRESTGetAgentDistinguishesAMissingProject(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    "ResourceNotFound",
				"message": "The project does not exist.",
			},
		})
	})

	_, err := c.GetAgent(context.Background(), "any")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("err = %v, want ErrProjectNotFound", err)
	}
	if errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want a missing project not to read as a missing agent", err)
	}
}

// The real agent-miss body, verified against a live project. Note the code is
// "not_found", where a missing project answers "ResourceNotFound".
func TestRESTGetAgentMissingAgentStillReadsAsNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    "not_found",
				"type":    "error",
				"message": "Agent support-pack doesn't exist [Request ID: abc123]",
			},
		})
	})

	_, err := c.GetAgent(context.Background(), "support-pack")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want ErrAgentNotFound", err)
	}
	if errors.Is(err, ErrProjectNotFound) {
		t.Errorf("err = %v, want a missing agent not to read as a missing project", err)
	}
}

// The error code is authoritative and the prose is not. This body is
// synthetic — it asserts the precedence rule rather than a shape Foundry is
// known to return — because prose is the part most likely to be reworded, and
// a rewording must never flip a missing agent into a missing project.
func TestRESTGetAgentCodeBeatsMessageText(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    "not_found",
				"message": "Agent x doesn't exist; the project does not exist check did not apply",
			},
		})
	})

	_, err := c.GetAgent(context.Background(), "x")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want ErrAgentNotFound", err)
	}
	if errors.Is(err, ErrProjectNotFound) {
		t.Errorf("err = %v, want the code to win over the message prose", err)
	}
}

// An empty or unparseable 404 body must still mean "no such agent" rather
// than failing the operation outright.
func TestRESTGetAgentEmptyNotFoundBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.GetAgent(context.Background(), "a")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err = %v, want ErrAgentNotFound", err)
	}
}

func TestRESTGetVersionDistinguishesAMissingProject(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"message": "The project does not exist."},
		})
	})

	_, err := c.GetVersion(context.Background(), "a", "1")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("err = %v, want ErrProjectNotFound", err)
	}
}
