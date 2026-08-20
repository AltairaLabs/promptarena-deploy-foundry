---
title: Invoke a deployed agent
description: Call a PromptKit pack running as an Azure AI Foundry hosted agent.
---

A deployed agent gets its own endpoint immediately — publishing is not
required. This page covers the `invocations` protocol, which is what the
bundled runtime serves.

## The endpoint

```
POST {account}.services.ai.azure.com/api/projects/{project}/agents/{agent}/endpoint/protocols/invocations?api-version=v1
```

The `/endpoint/protocols/` segment is easy to miss. Without it the request
still reaches the agent-serving backend and returns a **404 with an empty
body**, which looks like the agent does not exist.

## Quick start

```bash
export FOUNDRY_ACCOUNT=my-account
export FOUNDRY_PROJECT=my-project
export FOUNDRY_AGENT=my-pack

./scripts/invoke.sh "What is the capital of France?"
```

```json
{"output":"Paris is the capital of France."}
```

Streaming returns server-sent events, terminated by `[DONE]`:

```bash
./scripts/invoke.sh --stream "Name three primary colours"
```

```
data: {"delta":"Red"}
data: {"delta":", blue"}
data: {"delta":", yellow"}
data: [DONE]
```

## With curl

```bash
TOKEN=$(az account get-access-token --scope "https://ai.azure.com/.default" \
    --query accessToken -o tsv)

curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"Hello"}' \
  "https://${ACCOUNT}.services.ai.azure.com/api/projects/${PROJECT}/agents/${AGENT}/endpoint/protocols/invocations?api-version=v1"
```

## Request and response

The invocations contract is arbitrary JSON in and out — the platform relays it
without inspecting the schema — so this shape is the runtime's own.

| Field | Type | Meaning |
|---|---|---|
| `message` | string | The user's turn. `input` and `prompt` are accepted aliases. |
| `stream` | boolean | Return SSE deltas instead of a single JSON body. |
| `conversation_id` | string | Maps to a PromptKit session key. |

Unary responses are `{"output": "…"}`. Streaming responses are
`data: {"delta":"…"}` frames followed by `data: [DONE]`; a failure after
streaming has begun arrives as `data: {"error":"…"}`, because the HTTP status
has already been sent.

## Sessions

Each session is a VM-isolated sandbox with a persistent `$HOME`. The first call
creates one; the platform returns its id in the `x-agent-session-id` response
header.

Reuse it to keep the same sandbox — and note the id is read from the **query
string only**. A field in the body or an `x-agent-session-id` header is
forwarded to the container untouched but does not change which sandbox the
platform routes to.

```bash
./scripts/invoke.sh --session "$SESSION_ID" "and their hex codes?"
```

Sessions idle out after the configured `idle_timeout_minutes` (5–60, default
15), at which point compute is released and `$HOME` is preserved. Referencing
the session again restores it.

## Permissions

Invoking needs the `Microsoft.CognitiveServices/accounts/AIServices/agents/*`
data actions. **No built-in role grants them**, so create a custom role:

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

The agent also needs access to the model it calls. Each agent version gets its
**own** managed identity, so grant `Cognitive Services OpenAI User` to the
version's `instance_identity.principal_id` — a new version means a new
identity:

```bash
az role assignment create \
  --assignee-object-id "$INSTANCE_PRINCIPAL_ID" \
  --assignee-principal-type ServicePrincipal \
  --role "Cognitive Services OpenAI User" \
  --scope "$ACCOUNT_RESOURCE_ID"
```

Role assignments take a minute or two to propagate on the data plane.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `404`, empty body | The URL is missing `/endpoint/protocols/`. |
| `404`, `{"error":{"code":"ResourceNotFound"}}` | The project does not exist. |
| `404`, `{"error":{"code":"not_found"}}` | The agent does not exist. |
| `403`, `does not have permissions for …/agents/read` | The caller lacks the agents data actions. |
| `424 session_not_ready` | The container never answered `/readiness`. |
| `500`, `send (endpoint=… deployment=…)` | The turn reached the model and failed; the message names the resolved binding. |

A `session_not_ready` is the hardest of these, because container startup logs
are **not exposed through any API** — only lifecycle states. Two causes worth
checking first, both found by measurement:

- **The base image.** A distroless image never becomes ready; the identical
  binary on `debian-slim` answers in about two seconds.
- **The image must be `linux/amd64`** and live in an Azure Container Registry.

Note that a version reaching `active` proves only that the image **pulled**.
The platform does not start or probe the container until a session exists, so
an agent can report healthy and still not serve.
