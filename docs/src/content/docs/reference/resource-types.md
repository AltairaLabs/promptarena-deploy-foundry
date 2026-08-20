---
title: Resource types
description: The resources this adapter plans, creates and destroys.
---

A plan names four resource types. Three are real Azure objects; one is the
staged pack.

| Type | What it is |
|---|---|
| `agent` | The Foundry hosted agent. One per pack. |
| `agent_version` | One immutable version of that agent. |
| `served_version` | The endpoint selector pointing at a version. |
| `pack_object` | The pack staged to Blob storage, when it is too large to inline. |

## agent

Created once and then persists; only its versions churn. Its name is derived
from the pack id, sanitized to a conservative subset — lowercase alphanumerics
and interior hyphens, starting with a letter — because the name is a path
segment in the REST API and the legal character set is not documented.

Sanitizing is lossy, which is safe here: one pack maps to exactly one agent, so
there is never a second name to collide with.

Creating the agent also mints its **managed identity**, which is stable across
every version. Anything granted to it therefore survives a redeploy.

| Action | When |
|---|---|
| `CREATE` | No agent recorded in state, or the recorded one no longer exists. |
| `NO-CHANGE` | The agent exists. |

## agent_version

Every version is an immutable snapshot of the image, sizing, protocols and
environment. Changing a pack or its config creates a new one; there is no
in-place update.

Versions are billed, so one is created only when the pack hash or config hash
actually moves. The config hash covers the account, project, image, cpu, memory,
protocols, idle timeout, tags, resolved bindings, observability and tool specs —
everything that becomes part of the deployed version. It deliberately excludes
`dry_run`, `pack_inline_limit_bytes` and `staging_container`, which change
adapter behaviour or pack delivery rather than the running agent.

| Action | Detail |
|---|---|
| `CREATE` | `Create the first agent version`, `pack changed`, `config changed`, or both. |
| `UPDATE` | `Previous creation of version N did not finish; reconcile it`. |

A multi-member pack is noted in the detail — `(3 agents routed in-process)` —
because one Foundry agent serves them all, with PromptKit routing between
members rather than the platform.

### Provisioning

Creating a version returns before it is serving, so the adapter polls until the
status leaves `creating`. A version that ends `failed` surfaces the platform's
own reason, which matters because the likeliest failure is an image the project
cannot pull:

```
Container image not found. Verify the image name and tag exist in the registry
and that the workspace managed identity has access.
```

A version that never becomes active is recorded as **in-flight** and is
deliberately *not* served — pointing the endpoint at it would route traffic to a
container that is not running. The next apply reconciles it rather than creating
a duplicate alongside it.

Note that `active` means the image **pulled**. The platform does not start or
probe the container until a session exists.

## served_version

The endpoint's version selector: a single `FixedRatio` rule at 100% traffic.
Traffic splitting is not supported by the platform.

The same patch also declares the endpoint's **protocol list**, which is not
inherited from the version's `protocol_versions` and otherwise defaults to
`["responses"]`. An agent whose version declares `invocations` but whose
endpoint says `responses` is unreachable over the protocol it actually serves,
so both travel together in one merge-patch.

Prior versions are recorded newest-first, bounded to the most recent 20, which
makes a rollback a selector patch rather than an image rebuild.

## pack_object

Appears only when the serialized pack exceeds `pack_inline_limit_bytes`
(default 24576). Below that the pack is injected as an environment variable;
above it, it must be staged to Blob storage and the agent's identity needs read
access to the container.

Staging is not implemented yet. An oversized pack currently fails with an
actionable error rather than being staged silently.

## Destroy

Destroy removes the agent, which takes its versions with it. Deleting an agent
that is already gone is not an error — destroy converges on "gone" rather than
failing on an already-clean project.

## Status

| Status | Meaning |
|---|---|
| `deployed` | The agent exists and is serving a version. |
| `degraded` | It exists but serves nothing, or a version never finished provisioning. |
| `not_deployed` | Nothing recorded, or the recorded agent no longer exists. |
