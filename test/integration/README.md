# Deployed integration tests

These tests deploy a real hosted agent to a real Azure AI Foundry project.
**They create billable Azure resources.** They are behind the `integration`
build tag and skip unless all three environment variables below are set, so a
plain `go test ./...` never touches Azure.

## Running them

```bash
export FOUNDRY_TEST_ACCOUNT=my-foundry-account
export FOUNDRY_TEST_PROJECT=my-project
export FOUNDRY_TEST_IMAGE=myacr.azurecr.io/altairalabs/promptkit-foundry-runtime:v0.1.0

# Optional: the Azure OpenAI deployment name the pack binds to. Defaults to gpt-4o.
export FOUNDRY_TEST_MODEL=gpt-4o

make test-integration
```

CI does not run these — they need Entra credentials and create billable
resources. CI does type-check them (`go vet -tags=integration ./...`), so they
cannot silently stop compiling between manual runs.

## What they cover

| Test | What a failure means |
|------|----------------------|
| `ApplyCreatesAgentAndServesAVersion` | A deploy did not leave an agent, a version and an endpoint pointed at it. |
| `StatusReportsDeployed` | The adapter's view of a live deploy disagrees with Azure's. |
| `UnaryInvocation` | The container does not serve the invocations protocol, or the pack's system prompt never reached the model. |
| `ToolCalling` | The tool path is broken somewhere between model, arena mock and model. The tool returns a value the model cannot otherwise know. |
| `StreamingInvocation` | The SSE stream never sends its terminating frame, so clients reading to completion hang. |
| `SessionCarriesConversation` | The session id does not bind a conversation. It is read from the query string only, so a body field fails silently. |
| `ReapplyIsIdempotent` | An unchanged deploy churns a version. Foundry versions are immutable, so this costs a rollout for nothing. |
| `ChangedPackRollsTheServedVersion` | A changed pack does not produce a new version, or the endpoint is not repointed. |
| `DriftIsDetectedWhenTheAgentIsDeleted` | Plan does not notice an agent deleted outside the adapter, and apply would fail updating something that is gone. |
| `DestroyIsIdempotent` | A retried teardown fails, turning every interrupted destroy into manual cleanup. |

Each test deploys its own agent and deletes it on cleanup, including on
failure. A cleanup that cannot delete reports loudly rather than quietly
leaking a billable resource.

## Prerequisites

- An Entra identity with write access to the project's data plane, resolvable
  by `azidentity.DefaultAzureCredential` (`az login` is enough locally).
- The image must already be pushed to an Azure Container Registry the project
  can pull from. Foundry will not pull from ghcr.io.
- The image must be `linux/amd64`.

## Cleaning up

The tests delete the agents they create. If a run is interrupted, agents may be
left behind — list and remove them with the adapter's `destroy`, or delete them
from the Foundry portal.
