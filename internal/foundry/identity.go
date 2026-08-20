package foundry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Azure Resource Manager constants for granting an agent access to models.
//
// Only voice needs this. Text inference goes through the project endpoint,
// where the agent has implicit access; audio is not proxied there, so a pack
// with speech bindings has to reach the account endpoint and be authorized for
// it. Doing the grant here is what keeps a voice deploy a single command.
const (
	// armEndpoint is the Azure Resource Manager base URL.
	armEndpoint = "https://management.azure.com"
	// armScope is the Entra scope for ARM.
	armScope = "https://management.azure.com/.default"
	// openAIUserRoleID is the well-known id of Cognitive Services OpenAI User,
	// the least-privilege built-in role covering account-level OpenAI data
	// actions.
	openAIUserRoleID = "5e0bd9bd-7b93-4f28-af87-19fc36ad61bd"

	armSubscriptionsAPIVersion = "2020-01-01"
	armResourcesAPIVersion     = "2021-04-01"
	armRoleAssignAPIVersion    = "2022-04-01"

	// cognitiveAccountType is the resource type a Foundry account is.
	cognitiveAccountType = "Microsoft.CognitiveServices/accounts"
)

// ErrRoleAssignmentDenied reports that the deploying principal may not create
// role assignments. It is distinguished from other failures so Apply can hand
// the operator one command to run rather than failing the deploy.
var ErrRoleAssignmentDenied = errors.New("not permitted to create role assignments")

// armClient talks to Azure Resource Manager.
type armClient struct {
	baseURL  string
	pipeline runtime.Pipeline
}

// newARMGrantClient builds an ARM client using the ambient Azure credential.
func newARMGrantClient(_ context.Context) (*armClient, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve Azure credential: %w", err)
	}
	return newARMClient(armEndpoint, cred, nil)
}

// newARMClient builds an ARM client against an explicit base URL.
func newARMClient(
	baseURL string, cred azcore.TokenCredential, transport policy.Transporter,
) (*armClient, error) {
	opts := &policy.ClientOptions{}
	if transport != nil {
		opts.Transport = transport
	}

	authPolicy := runtime.NewBearerTokenPolicy(cred, []string{armScope}, nil)
	return &armClient{
		baseURL: baseURL,
		pipeline: runtime.NewPipeline(
			moduleName, Version,
			runtime.PipelineOptions{PerRetry: []policy.Policy{authPolicy}},
			opts,
		),
	}, nil
}

// GrantModelAccess gives an agent's identity the account-level model access
// that voice needs. Re-granting is a no-op.
func (c *armClient) GrantModelAccess(ctx context.Context, account, principalID string) error {
	scope, err := c.resolveAccountScope(ctx, account)
	if err != nil {
		return err
	}
	return c.ensureRoleAssignment(ctx, scope, principalID, openAIUserRoleID)
}

// resolveAccountScope finds a Foundry account's ARM id from its name.
//
// Searching for it means the deploy config needs no subscription id or
// resource group: the account name the adapter already has is enough.
func (c *armClient) resolveAccountScope(ctx context.Context, account string) (string, error) {
	subscriptions, err := c.listSubscriptions(ctx)
	if err != nil {
		return "", err
	}

	for _, sub := range subscriptions {
		id, found, findErr := c.findAccount(ctx, sub, account)
		if findErr != nil {
			// One inaccessible subscription must not stop the search.
			continue
		}
		if found {
			return id, nil
		}
	}

	return "", fmt.Errorf(
		"account %q was not found in any subscription this identity can read", account)
}

// listSubscriptions returns the subscription ids the caller can see.
func (c *armClient) listSubscriptions(ctx context.Context) ([]string, error) {
	reqURL := fmt.Sprintf("%s/subscriptions?api-version=%s", c.baseURL, armSubscriptionsAPIVersion)

	var page struct {
		Value []struct {
			SubscriptionID string `json:"subscriptionId"`
		} `json:"value"`
	}
	if err := c.get(ctx, reqURL, &page); err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}

	ids := make([]string, 0, len(page.Value))
	for _, s := range page.Value {
		ids = append(ids, s.SubscriptionID)
	}
	return ids, nil
}

// findAccount looks for the account in one subscription.
func (c *armClient) findAccount(
	ctx context.Context, subscription, account string,
) (id string, found bool, err error) {
	filter := fmt.Sprintf("resourceType eq '%s' and name eq '%s'", cognitiveAccountType, account)
	reqURL := fmt.Sprintf("%s/subscriptions/%s/resources?api-version=%s&$filter=%s",
		c.baseURL, subscription, armResourcesAPIVersion, escapeQuery(filter))

	var page struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := c.get(ctx, reqURL, &page); err != nil {
		return "", false, err
	}
	if len(page.Value) == 0 {
		return "", false, nil
	}
	return page.Value[0].ID, true, nil
}

// ensureRoleAssignment grants a role at a scope, treating an existing
// assignment as success so a redeploy does not fail on one already correct.
func (c *armClient) ensureRoleAssignment(
	ctx context.Context, scope, principalID, roleID string,
) error {
	// The subscription in the role definition id is the one owning the scope.
	subscription := subscriptionOf(scope)
	body := map[string]any{
		"properties": map[string]any{
			"roleDefinitionId": fmt.Sprintf(
				"/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/%s",
				subscription, roleID),
			"principalId": principalID,
			// Stating the type avoids Azure's replication-delay error when the
			// identity was created moments earlier, which is exactly the case
			// here: the agent is minted during this same apply.
			"principalType": "ServicePrincipal",
		},
	}

	reqURL := fmt.Sprintf("%s%s/providers/Microsoft.Authorization/roleAssignments/%s?api-version=%s",
		c.baseURL, scope, roleAssignmentName(scope, principalID, roleID), armRoleAssignAPIVersion)

	req, err := runtime.NewRequest(ctx, http.MethodPut, reqURL)
	if err != nil {
		return fmt.Errorf("build role assignment: %w", err)
	}
	if marshalErr := runtime.MarshalAsJSON(req, body); marshalErr != nil {
		return fmt.Errorf("encode role assignment: %w", marshalErr)
	}

	resp, err := c.pipeline.Do(req)
	if err != nil {
		return fmt.Errorf("create role assignment: %w", err)
	}
	defer closeBody(resp)

	switch {
	case runtime.HasStatusCode(resp, http.StatusOK, http.StatusCreated):
		return nil
	case runtime.HasStatusCode(resp, http.StatusConflict):
		// Already granted.
		return nil
	case runtime.HasStatusCode(resp, http.StatusForbidden, http.StatusUnauthorized):
		return fmt.Errorf("granting %s at %s: %w", roleID, scope, ErrRoleAssignmentDenied)
	default:
		return runtime.NewResponseError(resp)
	}
}

// get performs a GET and decodes the body.
func (c *armClient) get(ctx context.Context, reqURL string, into any) error {
	req, err := runtime.NewRequest(ctx, http.MethodGet, reqURL)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := c.pipeline.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer closeBody(resp)

	if !runtime.HasStatusCode(resp, http.StatusOK) {
		return runtime.NewResponseError(resp)
	}
	if err := runtime.UnmarshalAsJSON(resp, into); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// roleAssignmentName derives a stable GUID for an assignment, so repeated
// applies address the same object rather than littering new ones.
func roleAssignmentName(scope, principalID, roleID string) string {
	sum := sha256.Sum256([]byte(scope + "|" + principalID + "|" + roleID))

	// Shape the digest into a v4-looking GUID. Azure only requires a valid
	// GUID; determinism is what matters here.
	const (
		versionMask, versionBits = 0x0f, 0x40
		variantMask, variantBits = 0x3f, 0x80
	)
	sum[6] = (sum[6] & versionMask) | versionBits
	sum[8] = (sum[8] & variantMask) | variantBits

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// subscriptionOf extracts the subscription id from an ARM resource id.
func subscriptionOf(scope string) string {
	const prefix = "/subscriptions/"
	rest, found := cutPrefix(scope, prefix)
	if !found {
		return ""
	}
	id, _, _ := cutAt(rest, '/')
	return id
}

// manualGrantCommand renders the command an operator runs when the adapter
// cannot create the assignment itself.
func manualGrantCommand(account, principalID string) string {
	return fmt.Sprintf(
		"az role assignment create --assignee-object-id %s "+
			"--assignee-principal-type ServicePrincipal "+
			"--role \"Cognitive Services OpenAI User\" "+
			"--scope $(az cognitiveservices account show --name %s "+
			"--query id -o tsv --resource-group <resource-group>)",
		principalID, account)
}

// escapeQuery percent-encodes an ARM $filter value.
func escapeQuery(s string) string {
	return url.QueryEscape(s)
}

// cutPrefix reports whether s starts with prefix and returns the remainder.
func cutPrefix(s, prefix string) (after string, found bool) {
	return strings.CutPrefix(s, prefix)
}

// cutAt splits s at the first occurrence of sep.
func cutAt(s string, sep byte) (before, after string, found bool) {
	return strings.Cut(s, string(sep))
}
