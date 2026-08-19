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

make test-integration
```

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
