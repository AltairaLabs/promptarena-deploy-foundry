---
title: Your first deployment
description: Deploy a single-agent prompt pack to an Azure AI Foundry hosted agent end to end
---

By the end of this tutorial a real Foundry hosted agent will answer a real
question using a real Azure OpenAI deployment.

Two of the steps below exist because Foundry fails **after** the agent version
is created rather than at validation time. A version can reach `active` and
still never serve a request.

## Prerequisites

- An Azure subscription, and `az` authenticated:
  ```bash
  az login
  ```
- A **project-enabled** Foundry account (`allowProjectManagement: true`) with a
  project in it. An ordinary multi-service Cognitive Services account cannot
  host agents.
- A model deployed in that account — this tutorial uses one named
  `gpt-4-1-mini`.
- The adapter installed:
  ```bash
  promptarena deploy adapter install foundry
  ```

## Step 1: Give yourself the agents data actions

Creating and invoking hosted agents needs
`Microsoft.CognitiveServices/accounts/AIServices/agents/*`. **No built-in role
grants those**, so create a custom role once per subscription:

```bash
az role definition create --role-definition '{
  "Name": "Foundry Hosted Agent Operator",
  "IsCustom": true,
  "Description": "Manage and invoke Foundry hosted agents.",
  "Actions": [],
  "DataActions": ["Microsoft.CognitiveServices/accounts/AIServices/agents/*"],
  "AssignableScopes": ["/subscriptions/<sub>/resourceGroups/<rg>"]
}'
```

Then assign it to yourself on the account. Without it every call returns `403`
naming `…/agents/read`.

## Step 2: Mirror the runtime image into an Azure Container Registry

Foundry **will not pull from `ghcr.io`**. The published image has to be copied
into an ACR the project can reach:

```bash
az acr import --name myacr \
  --source ghcr.io/altairalabs/promptkit-foundry-runtime:v0.1.0 \
  --image altairalabs/promptkit-foundry-runtime:v0.1.0
```

Note the image tag carries a leading `v` (`v0.1.0`), matching the release tag.

## Step 3: Let the project pull it

This is the grant that fails late. The **project's** managed identity needs
`Container Registry Repository Reader` (or `AcrPull`) on the registry. Without
it, `apply` reports:

```
Container image not found. Verify the image name and tag exist in the registry
and that the workspace managed identity has access.
```

:::note[Three identities, and only one of them is yours]
You create the agent; the **project's** identity pulls the image; the
**agent's** identity calls the model. This step is about the second. The third
needs nothing at all for text — see step 5.
:::

## Step 4: Configure the deploy target

In your arena config:

```yaml
deploy:
  provider: foundry
  config:
    account: my-account
    project: my-project
    image: myacr.azurecr.io/altairalabs/promptkit-foundry-runtime:v0.1.0
    cpu: "1"
    memory: "2Gi"
    providers:
      - name: default
        role: llm
        type: openai
        model: gpt-4-1-mini
```

Three things that catch people:

- **`type` is the provider family, not the platform.** Use `openai`. Setting
  `azure` fails with `unsupported provider type: azure`.
- **`model` is the Azure *deployment* name**, not the model id. A model deployed
  as `gpt-4-1-mini` is referenced by that, not `gpt-4.1-mini`.
- **`cpu` and `memory` are a fixed pair.** Only `0.5`/`1Gi`, `1`/`2Gi` and
  `2`/`4Gi` are legal, and they are immutable once a version exists. A mismatch
  fails at plan time rather than after the resource is created.

## Step 5: What you do *not* need to grant

Nothing. A deployed agent reaches its model with **no role assignment of any
kind**, because Foundry grants an agent implicit access to model inferencing
through its own project endpoint, and the runtime takes that route deliberately.

This was verified both ways against a live project: a brand-new agent with zero
role assignments answers over the project route, while the account endpoint
(`{account}.openai.azure.com`) returns 404 until its identity is granted
`Cognitive Services OpenAI User` by hand.

Voice is the exception — see [Invoke a deployed agent](/how-to/invoke/).

## Step 6: Plan

```bash
promptarena deploy plan
```

`plan` reads the account and project to check they exist, then diffs the pack
and config hashes against prior adapter state:

```
3 to create

  + agent.my-pack (Create the Foundry hosted agent)
  + agent_version.my-pack (Create the first agent version)
  + served_version.endpoint selector (Point the endpoint at the first version at 100% traffic)
```

A typo in `project` is caught here, before anything is created:

```
project "typo" does not exist in account "my-account"; check the deploy config
```

## Step 7: Apply

```bash
promptarena deploy apply
```

Apply creates the agent, creates an immutable version, polls until the version
leaves `creating`, then points the endpoint selector at it at 100% traffic.

If the version never becomes active it is recorded as **in-flight** and
deliberately not served — pointing the endpoint at a container that is not
running would only route traffic into a hole. The next apply reconciles it
rather than creating a duplicate.

:::caution[`active` means the image pulled, and nothing more]
The platform validates the image when the version is created, but does **not**
start or probe your container until a session exists. A deployment reporting
healthy is not evidence that it serves. The first invocation is the real test.
:::

## Step 8: Ask it something

```bash
export FOUNDRY_ACCOUNT=my-account
export FOUNDRY_PROJECT=my-project
export FOUNDRY_AGENT=my-pack

./scripts/invoke.sh "What is the capital of France?"
```

```json
{"output":"Paris is the capital of France."}
```

The first call cold-starts a session sandbox and takes a few seconds. If it
returns `424 session_not_ready`, the container never answered `/readiness` —
see the troubleshooting table in [Invoke a deployed
agent](/how-to/invoke/).

## Step 9: Check status

```bash
promptarena deploy status
```

| Status | Meaning |
|---|---|
| `deployed` | The agent exists and is serving a version. |
| `degraded` | It exists but serves nothing, or a version never finished provisioning. |
| `not_deployed` | Nothing recorded, or the recorded agent is gone. |

## Step 10: Clean up

```bash
promptarena deploy destroy
```

Destroy removes the agent, which takes all its versions with it. Destroying an
agent that is already gone is not an error — it converges on "gone" rather than
failing on an already-clean project.

Agent versions themselves are billed, so a pack you are iterating on is worth
destroying between sessions.

## What next

- [Deploying a multi-agent pack](/tutorials/02-multi-agent/) — one agent, many prompts
- [Invoke a deployed agent](/how-to/invoke/) — streaming, sessions, troubleshooting
- [Configuration reference](/reference/configuration/) — every field
- [Preview changes without deploying](/how-to/dry-run/)
