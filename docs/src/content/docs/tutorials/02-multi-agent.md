---
title: Deploying a multi-agent pack
description: One Foundry agent serves the whole pack, with PromptKit routing between members in-process
---

A pack with several prompts deploys as **one** Foundry hosted agent, not one per
prompt. PromptKit routes between the members inside the container.

This is the opposite of how some other deploy adapters work, and it changes what
you configure, what you pay for, and how you call it.

:::note[One agent, many prompts]
The platform sees a single agent with a single endpoint. Everything about
multi-agent behaviour — routing, handoffs, workflow state — happens *inside* the
container, where the whole PromptKit pipeline is running.
:::

## The pack

Two prompts, and an `entry` that names where a turn starts:

```json
{
  "id": "support-desk",
  "agents": { "entry": "triage" },
  "prompts": {
    "triage": {
      "id": "triage",
      "system_template": "Classify the request as billing, technical, or other. Answer with one word."
    },
    "billing": {
      "id": "billing",
      "system_template": "You are a billing specialist. Be precise about amounts and dates."
    }
  }
}
```

:::caution[A multi-prompt pack must define `agents.entry`]
The runtime resolves which prompt serves a turn in this order: the
`PROMPTPACK_AGENT` environment variable, then `agents.entry`, then — only if the
pack has exactly one prompt — that prompt.

The adapter deliberately never sets `PROMPTPACK_AGENT`, because one agent serves
the whole pack. So a pack with two or more prompts and no `agents.entry` fails
at container start with:

```
cannot determine agent name: set PROMPTPACK_AGENT, define agents.entry in the
pack, or ensure the pack has exactly one prompt
```

That failure surfaces as `424 session_not_ready` on the first invocation, *after*
a successful deploy — the agent version reaches `active` regardless, because
`active` only means the image pulled.
:::

## Deploy config

Nothing changes for multi-agent. The same block deploys the whole pack:

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

There are no per-prompt overrides — sizing, image and provider bindings apply to
the pack as a whole, because there is only one container.

## Plan

```bash
promptarena deploy plan
```

The member count appears in the version detail:

```
3 to create

  + agent.support-desk (Create the Foundry hosted agent)
  + agent_version.support-desk (Create the first agent version (2 agents routed in-process))
  + served_version.endpoint selector (Point the endpoint at the first version at 100% traffic)
```

That parenthetical is informational, not a warning. It is there so a
multi-prompt pack does not look like it silently dropped its other members.

## Apply and call it

```bash
promptarena deploy apply
```

One agent, one endpoint. A turn enters at `triage`:

```bash
export FOUNDRY_ACCOUNT=my-account
export FOUNDRY_PROJECT=my-project
export FOUNDRY_AGENT=support-desk

./scripts/invoke.sh "my invoice is wrong"
```

```json
{"output":"billing"}
```

## Keeping context across turns

Because the whole pack runs in one container, a multi-turn conversation stays in
one session sandbox. Reuse the session id returned in the `x-agent-session-id`
response header:

```bash
./scripts/invoke.sh --session "$SESSION_ID" "so what should I do about it?"
```

The session id is read from the **query string only**. Putting it in the body or
a header forwards it to your container but does not change which sandbox the
platform routes to — so the second turn would land in a fresh sandbox and lose
its context.

## What changes when a prompt leaves the pack

Removing `billing` changes the pack hash, so the next apply creates a **new
version** of the same agent:

```
1 to create, 1 to update, 1 unchanged

    agent.support-desk (Up to date)
  + agent_version.support-desk (pack changed)
  ~ served_version.endpoint selector (Point the endpoint at the new version at 100% traffic)
```

The member suffix is gone because the pack is down to one prompt — it only
appears for two or more. The agent line is unchanged: the agent shell is created
once and then persists, and only its versions churn.

Versions are immutable, so there is no in-place edit. The endpoint selector is
then patched to the new version at 100% traffic; traffic splitting is not
supported by the platform. Prior versions are recorded newest-first, which makes
a rollback a selector patch rather than an image rebuild.

The agent itself — and its managed identity — is untouched, so anything granted
to that identity survives every redeploy.

## Cost

One pack is one agent, however many prompts it has, so member count does not
multiply the deployment. What is billed is **versions**, and a new one is
created whenever the pack hash or config hash moves. The adapter only creates a
version when one of those actually changed, so re-applying an unchanged pack is
a no-op.

## What next

- [Invoke a deployed agent](/how-to/invoke/) — streaming, sessions, troubleshooting
- [Resource types](/reference/resource-types/) — what `agent`, `agent_version` and `served_version` each mean
- [How the adapter decides what changed](/explanation/resource-lifecycle/)
