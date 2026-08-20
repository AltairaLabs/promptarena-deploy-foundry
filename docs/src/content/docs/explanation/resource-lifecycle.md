---
title: Resource lifecycle
description: How a pack becomes a running agent, and what happens on redeploy.
---

One pack maps to **one agent with N immutable versions**. That shape follows
from the platform: versions cannot be edited, and an endpoint serves exactly one
of them at a time.

## A deploy

```
apply
 ├─ create the agent          (once, ever)
 │   └─ mint its managed identity
 ├─ create a version          (whenever the pack or config hash moves)
 │   └─ poll until active or failed
 └─ point the endpoint at it  (selector + protocols, one patch)
```

The agent is created once and persists. Everything that changes thereafter
changes a version.

## Why one agent, not one per pack member

A multi-agent pack could map to one Foundry agent per member. It does not.
PromptKit's own composition and workflow layers route between members
**in-process**, which gives one resource, one endpoint, one identity, no
inter-agent network hops, and routing semantics identical to running locally.

The trade-off is no per-member scaling and no separate per-member endpoints.
That costs little here, because Foundry scales per *session* rather than per
replica anyway — a busy member does not need its own replica count.

## What decides that a version is needed

Three inputs: the pack hash, the config hash, and prior adapter state.

The pack is hashed **as received**, not re-serialized, because re-encoding could
reorder keys and produce a spurious diff. The config hash covers everything that
becomes part of the deployed version, and deliberately excludes settings that
change only adapter behaviour.

If neither hash has moved, no version is created. Versions are immutable and
billed; creating one per apply would be waste, not caution.

## State, and why it is load-bearing

The adapter persists a small state document between operations: the agent name,
the served version, prior versions, the two hashes, and any in-flight version.

None of it can be recomputed. The agent name is a *sanitized* form of the pack
id, so it cannot be derived reliably in reverse; the served version is assigned
by the platform. Unknown fields survive a parse-and-rewrite cycle, so a newer
adapter's state is not silently truncated by an older one reading it.

## Drift

Because state can disagree with reality, `plan` verifies it against the live
control plane. An agent deleted out of band plans as a create, with the reason
stated, rather than reporting a no-op against something that does not exist.

Two distinctions matter here:

- **A 403 is not a 404.** Treating a permissions or throttling failure as
  "does not exist" would turn a transient problem into a spurious create.
- **A missing project is not drift.** Foundry answers 404 for a missing agent
  and a missing project alike, but the second is a configuration error that
  every operation would hit, so plan stops rather than proposing work.

## Partial failure

Apply returns state **even when it fails**. An agent created before a later step
failed is recorded, so the next apply finds it instead of orphaning it in the
project with nothing pointing at it.

A version that provisioned but never became active is recorded as in-flight and
not served. The next apply reconciles that version rather than creating a
duplicate beside it.

## Rollback

Prior versions are recorded newest-first. Because versions are immutable and the
endpoint selector is a single patch, returning to an earlier one is a selector
change — not an image rebuild and redeploy.

## Teardown

Destroy removes the agent, and its versions go with it. Destroying something
already gone succeeds: teardown converges on "gone" rather than failing on an
already-clean project.

Sessions are separate. The platform deprovisions a session's compute after its
idle timeout, preserving `$HOME`, and deletes the session entirely after 30 days
of inactivity.
