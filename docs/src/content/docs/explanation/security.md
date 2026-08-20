---
title: Security
description: Identities, permissions and secrets in a Foundry deployment.
---

The guiding property is that **no secret reaches the container**. Everything the
deployed agent does, it does as its own managed identity.

## Three identities

| Identity | Used for |
|---|---|
| **You**, the deployer | Creating agents and versions, and invoking them |
| **The project's** managed identity | Pulling the image from your registry |
| **The agent's** managed identity | Calling models and session storage at runtime |

The agent's identity is minted with the agent and is **stable across every
version**, so anything granted to it survives a redeploy.

## What the deployer needs

Creating and updating agents is a data-plane operation requiring
`Microsoft.CognitiveServices/accounts/AIServices/agents/*`. **No built-in role
grants those**, so a custom role is needed:

```bash
az role definition create --role-definition '{
  "Name": "Foundry Hosted Agent Operator",
  "IsCustom": true,
  "Actions": [],
  "DataActions": ["Microsoft.CognitiveServices/accounts/AIServices/agents/*"],
  "AssignableScopes": ["/subscriptions/<sub>/resourceGroups/<rg>"]
}'
```

Callers that only need to *invoke* an agent should get the least-privilege
built-in role instead — `Foundry Agent Consumer` — rather than the operator
role.

## What the agent needs

For **text, nothing.** Inference goes through the project endpoint, where
Foundry grants an agent implicit access to model inferencing within its own
project. The runtime takes that route deliberately, and a deploy therefore
needs no role assignment at all.

For **voice**, one grant. Audio is not proxied by the project endpoint —
`/audio/speech` and `/audio/transcriptions` are served only at the account
endpoint — so speech bindings need `Cognitive Services OpenAI User` on the
account.

Apply makes that assignment itself when the pack declares speech bindings. It
finds the account from its name, so nothing extra goes in the config, and the
assignment is idempotent and made once per agent. Doing so needs
`Microsoft.Authorization/roleAssignments/write` on the account — Owner or Role
Based Access Control Administrator.

If the deployer lacks that, **the deploy still succeeds** and prints the exact
command for someone who has it. The agent exists and its text path works;
refusing to finish over a permission someone else can grant would be worse.

## Secrets

Do not put secrets in the image or in environment variables. Environment is
immutable per version and visible on the version object.

The adapter injects no credential of any kind. Provider bindings carry a
deployment name, not a key, and the runtime authenticates with the identity
Foundry gives the container. Where a service insists on a non-empty API key to
construct, a placeholder is used and never sent — the outbound request's
`Authorization` header is replaced with a managed-identity token.

For secrets your own code needs, use Foundry **project connections**, referenced
as `${{connections.<name>.credentials.<field>}}`, rather than plain environment.

## The container

- **`linux/amd64` only.** arm64 is rejected at version-create time.
- **Azure Container Registry only.** Foundry will not pull from public
  registries; mirror the image into your own ACR and grant the project's
  identity `Container Registry Repository Reader`.
- Restrict pull access to that identity, and prefer a network-secured registry
  where your project supports it. Projects created after 25 June 2026 support
  one; earlier projects require the registry to be publicly reachable.
- **The base image matters.** A distroless image never becomes ready in the
  sandbox. The published runtime uses `debian-slim` and runs unprivileged.

## Isolation

Every session is a **VM-isolated sandbox** with its own filesystem. Sessions are
isolated from one another, and the platform scopes them to the calling identity
by default, so one caller cannot see or act on another's.

For a middle-tier service acting for many end users, sessions can be partitioned
further with an isolation key. Delegating an end-user identity via the
`x-ms-user-identity` header needs a data action that **no built-in role
grants** — it must be a custom role, assigned only to a service you trust to
assert who its users are.

## Supply chain

`gosec` runs on every pull request via golangci-lint, Dependabot tracks Go and
GitHub Actions dependencies, and **every** Action in **every** workflow is
pinned by commit SHA — including the runtime-image publish, which is the one
workflow holding `packages: write`.

Pinning by tag is not equivalent. A tag is mutable, so `@v6` silently follows
whatever that tag points at; a compromised or retagged release reaches a
workflow that can push to the registry operators mirror from. Dependabot still
raises the version bumps, but against the pinned SHA rather than around it.

## Reporting a vulnerability

Email [security@altairalabs.ai](mailto:security@altairalabs.ai). Please do not
open a public issue — see [SECURITY.md](https://github.com/AltairaLabs/promptarena-deploy-foundry/blob/main/SECURITY.md).
