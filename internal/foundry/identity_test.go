package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// armTestClient wires an armClient to an httptest server.
func armTestClient(t *testing.T, handler http.HandlerFunc) *armClient {
	t.Helper()
	srv := newTLSServer(t, handler)

	c, err := newARMClient(srv.URL, fakeCredential{}, srv.Client())
	if err != nil {
		t.Fatalf("newARMClient: %v", err)
	}
	return c
}

// The account's ARM id is derived from the account name alone, so granting
// needs no subscription or resource group in the deploy config.
func TestResolveAccountScope(t *testing.T) {
	const want = "/subscriptions/sub-1/resourceGroups/rg/providers/" +
		"Microsoft.CognitiveServices/accounts/acct"

	c := armTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/subscriptions"):
			writeJSON(t, w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{"subscriptionId": "sub-1"}},
			})
		default:
			writeJSON(t, w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{"id": want}},
			})
		}
	})

	got, err := c.resolveAccountScope(context.Background(), "acct")
	if err != nil {
		t.Fatalf("resolveAccountScope: %v", err)
	}
	if got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
}

// An account that exists in no visible subscription must say so rather than
// produce a scope that silently grants nothing.
func TestResolveAccountScopeNotFound(t *testing.T) {
	c := armTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/subscriptions") {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{"subscriptionId": "sub-1"}},
			})
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"value": []any{}})
	})

	if _, err := c.resolveAccountScope(context.Background(), "missing"); err == nil {
		t.Fatal("resolveAccountScope succeeded for an account in no subscription")
	}
}

func TestEnsureRoleAssignmentSendsTheRole(t *testing.T) {
	var body map[string]any
	var gotMethod string

	c := armTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		decodeBody(t, r, &body)
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": "ra"})
	})

	err := c.ensureRoleAssignment(context.Background(),
		"/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a",
		"principal-1", openAIUserRoleID)
	if err != nil {
		t.Fatalf("ensureRoleAssignment: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	props, ok := body["properties"].(map[string]any)
	if !ok {
		t.Fatalf("body has no properties: %v", body)
	}
	if props["principalId"] != "principal-1" {
		t.Errorf("principalId = %v, want principal-1", props["principalId"])
	}
	if !strings.Contains(props["roleDefinitionId"].(string), openAIUserRoleID) {
		t.Errorf("roleDefinitionId = %v, want it to name the role", props["roleDefinitionId"])
	}
	// The principal is a managed identity, and Azure rejects the assignment
	// with a replication error when the type is not stated.
	if props["principalType"] != "ServicePrincipal" {
		t.Errorf("principalType = %v, want ServicePrincipal", props["principalType"])
	}
}

// Re-granting must be a no-op, so a redeploy does not fail on an assignment
// that is already correct.
func TestEnsureRoleAssignmentTreatsConflictAsSuccess(t *testing.T) {
	c := armTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]any{
			"error": map[string]any{"code": "RoleAssignmentExists"},
		})
	})

	err := c.ensureRoleAssignment(context.Background(), "/scope", "p", openAIUserRoleID)
	if err != nil {
		t.Errorf("ensureRoleAssignment = %v, want a conflict treated as success", err)
	}
}

// A deployer without roleAssignments/write must get a distinguishable error,
// so Apply can tell them the one command to run instead of failing the deploy.
func TestEnsureRoleAssignmentReportsForbidden(t *testing.T) {
	c := armTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{
			"error": map[string]any{"code": "AuthorizationFailed"},
		})
	})

	err := c.ensureRoleAssignment(context.Background(), "/scope", "p", openAIUserRoleID)
	if !errors.Is(err, ErrRoleAssignmentDenied) {
		t.Errorf("err = %v, want ErrRoleAssignmentDenied", err)
	}
}

// The assignment name must be stable for a given principal, scope and role, so
// repeated applies address the same object rather than littering new ones.
func TestRoleAssignmentNameIsDeterministic(t *testing.T) {
	a := roleAssignmentName("/scope", "principal", openAIUserRoleID)
	b := roleAssignmentName("/scope", "principal", openAIUserRoleID)

	if a != b {
		t.Errorf("name is not stable: %q vs %q", a, b)
	}
	if a == roleAssignmentName("/scope", "other", openAIUserRoleID) {
		t.Error("different principals produced the same assignment name")
	}
	// Azure requires a GUID.
	if len(a) != 36 || strings.Count(a, "-") != 4 {
		t.Errorf("name = %q, want a GUID", a)
	}
}

// grantModelAccess is what Apply calls; it must report a denial without
// pretending the grant happened.
func TestGrantModelAccessSurfacesDenial(t *testing.T) {
	c := armTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/subscriptions"):
			writeJSON(t, w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{"subscriptionId": "sub-1"}},
			})
		case r.Method == http.MethodGet:
			writeJSON(t, w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{"id": "/subscriptions/sub-1/x"}},
			})
		default:
			writeJSON(t, w, http.StatusForbidden, map[string]any{})
		}
	})

	err := c.GrantModelAccess(context.Background(), "acct", "principal-1")
	if !errors.Is(err, ErrRoleAssignmentDenied) {
		t.Errorf("err = %v, want ErrRoleAssignmentDenied", err)
	}
}

// manualGrantCommand is what the operator is handed when the adapter cannot do
// it, so it has to be complete enough to paste.
func TestManualGrantCommand(t *testing.T) {
	got := manualGrantCommand("acct", "principal-1")

	for _, want := range []string{"az role assignment create", "principal-1", "acct"} {
		if !strings.Contains(got, want) {
			t.Errorf("command = %q, want it to contain %q", got, want)
		}
	}
}

// decodeJSONBody is a convenience for the assertions above.
func decodeJSONBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}
