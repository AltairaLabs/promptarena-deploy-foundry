---
title: Resource types
description: The resources this adapter plans, creates and destroys.
---

A plan names five resource types. Three are real Azure objects; one is a
version recorded as staged; one is the staged pack.

| Type | What it is |
|---|---|
| `agent` | The Foundry hosted agent. One per pack. |
| `agent_version` | One immutable version of that agent. |
| `served_version` | The endpoint selector pointing at a version. |
| `staged_version` | A version created by a `rollout_stage: candidate` apply that the endpoint does not serve. |
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
`dry_run`, `rollout_stage`, `pack_inline_limit_bytes` and `staging_container`,
which change adapter behaviour or pack delivery rather than the running agent.

| Action | Detail |
|---|---|
| `CREATE` | `Create the first agent version`, `pack changed`, `config changed`, or both. With `rollout_stage: candidate`, `Create a version to stage without serving it`. |
| `UPDATE` | `Previous creation of version N did not finish; create a new version`. |

Each version carries `pack_version` metadata, the compiled pack's `version`
field; see [Version metadata](/reference/configuration/#version-metadata).

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
container that is not running. Status reports the deployment as degraded, and
the next apply creates a fresh version to serve.

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

| Action | When |
|---|---|
| `CREATE` | The first version is served. |
| `UPDATE` | A new version is served, or `rollout_stage: promote` serves the staged one. |
| `NO-CHANGE` | The endpoint keeps serving the version it does. |

## staged_version

A version created with `rollout_stage: candidate`. It exists on the platform and
can be reached by a session created on it, but the endpoint does not serve it
and new sessions do not use it. Unlike an in-flight version it is not a fault:
status reports it healthy.

| Action | When |
|---|---|
| `CREATE` | A candidate apply stages a version and none was staged. |
| `UPDATE` | A candidate apply stages a version in place of a staged version built from another pack or config, or a promote apply serves the staged version and clears the record. |
| `NO-CHANGE` | A version is staged and this apply leaves it alone. |

A promote apply serves it and clears the record; a failed or deleted staged
version is dropped by the promote that finds it. See
[Staged rollouts](/reference/configuration/#staged-rollouts).

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
| `deployed` | The agent exists and is serving a version. A staged version does not change this. |
| `degraded` | It exists but serves nothing, or a version never finished provisioning. |
| `not_deployed` | Nothing recorded, or the recorded agent no longer exists. |
