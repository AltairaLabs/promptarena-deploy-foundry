---
title: Preview a deploy
description: Use plan and dry run to see what a deploy would change.
---

Two things let you see what a deploy would do before it does it: `plan`, which
reports the changes, and `dry_run`, which makes `apply` simulate them.

## Plan

```bash
promptarena deploy plan
```

```
3 to create
  agent           smoke-pack        CREATE   Create the Foundry hosted agent
  agent_version   smoke-pack        CREATE   Create the first agent version
  served_version  endpoint selector CREATE   Point the endpoint at the first version at 100% traffic
```

Plan reads three things — the pack hash, the config hash and prior adapter
state — and then **verifies that state against the live control plane** so
drift is visible. If the agent was deleted outside the adapter, the plan says
so rather than reporting a no-op against something that is gone:

```
agent "smoke-pack" is recorded in state but no longer exists; it will be recreated
```

A second plan against unchanged inputs reports `No changes`. Versions are
immutable and billed, so one is created only when the pack or config hash
actually moves.

Verification is best-effort. A control plane that cannot be reached degrades to
an unverified plan with a warning, rather than failing — a plan you cannot run
is worse than one carrying a caveat:

```
prior state could not be verified against the control plane (…); the plan
assumes the recorded state is accurate
```

A missing **project** is the exception. That is a configuration error rather
than drift, and every operation against it would fail, so plan stops.

## Dry run

```yaml
deploy:
  provider: foundry
  config:
    dry_run: true
```

With `dry_run` set, `apply` runs its full sequence against an in-memory
simulation of the control plane: it creates an agent, creates a version, and
promotes it, returning state shaped exactly as a real apply would. Nothing
reaches Azure and no credential is needed.

That means the adapter's own logic — sequencing, hash comparison, in-flight
reconciliation, state transitions — is exercised for real. It is the difference
between "the plan looks right" and "apply would do the right thing".

Dry run also skips control-plane verification, so it stays **fully offline**.
The trade-off is that it cannot tell you anything about Azure: an image that
does not exist, a registry the project cannot pull from, or a quota you have
exhausted are all invisible until a real apply.

## What a plan cannot tell you

A version reaching `active` proves the image was **pulled**, and nothing more.
The platform does not start or probe your container until a session exists, so
an agent can report healthy and still not serve. Invoking it is the only proof
that it works — see [invoking a deployed agent](invoke).

## Warnings worth reading

Plan surfaces problems that would otherwise appear minutes into an apply, or
not at all:

- **`a2a__` tools in the pack.** These are remote calls to another agent over
  HTTP. Serving the whole pack from one Foundry agent does not resolve them, so
  they will fail at runtime.
- **`invocations_ws` at 0.5 vCPU.** Microsoft recommends at least 1 vCPU / 2 GiB
  for real-time voice; below that audio degrades rather than failing outright,
  which is harder to notice.
- **An oversized pack.** Above the inline limit the pack must be staged, and the
  agent's identity needs read access to the staging container.
- **A served version that does not match state**, which means the endpoint was
  repointed outside the adapter.
