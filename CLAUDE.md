# Foundry Deploy Adapter - Claude Code Project Instructions

## Project Overview

This is the Azure AI Foundry deploy adapter for PromptKit. It implements the
`deploy.Provider` interface as a JSON-RPC 2.0 subprocess that PromptKit
discovers and invokes, and it deploys a pack as a Foundry **hosted agent** —
a bring-your-own-container agent on Microsoft-managed infrastructure.

Two binaries:

| Binary | Purpose |
|---|---|
| `promptarena-deploy-foundry` | The adapter plugin (stdio JSON-RPC) |
| `foundry-runtime` | Container entrypoint serving the Foundry protocol contracts |

The design of record lives in `docs/local-backlog/` (gitignored). Read it
before making structural changes — it records which platform facts are
documented, which are inferred, and which still need a real deploy to settle.

## Git Workflow

- **Never push directly to main** — use feature branches.
- Branch naming: `feat/<description>`, `fix/<description>`, or `feature/<issue-number>-<short-description>`.
- Standard flow: branch → commit → push with `-u` → create PR via `gh pr create` → monitor CI → merge via `gh pr merge --squash`.
- Use conventional commits (`feat:`, `fix:`, `chore:`, `ci:`, `docs:`) and sign off with `git commit -s` (DCO is enforced).
- When continuing a previous session, check `git status`, `git log --oneline -5`, and any existing plan files before taking action.

## Build & Test Commands

`GOWORK=off` is used throughout so a sibling `go.work` never leaks into the
build.

```bash
make fmt          # goimports -local github.com/AltairaLabs/promptarena-deploy-foundry
make lint         # golangci-lint
make test         # go test ./... -race -count=1
make build        # adapter + runtime binaries
make check        # fmt + lint + test + build
make docker-build # linux/amd64 runtime image
make install-hooks

# Run the adapter (JSON-RPC over stdio)
echo '{"jsonrpc":"2.0","method":"get_provider_info","id":1}' | ./promptarena-deploy-foundry
```

## Project Structure

| Path | Purpose |
|------|---------|
| `main.go` | Entry point — thin wrapper calling `adaptersdk.Serve(provider)` |
| `internal/foundry/provider.go` | `Provider` and the seven `deploy.Provider` methods |
| `internal/foundry/schema.go` | JSON Schema for the provider config |
| `internal/foundry/errors.go` | Sentinel errors, matched with `errors.Is` |
| `internal/foundry/version.go` | Build-time version variables (ldflags) |
| `cmd/foundry-runtime/` | Container entrypoint; `/readiness`, `/invocations`, `/responses`, `/invocations_ws` |
| `Dockerfile` | linux/amd64 runtime image (distroless) |
| `test/integration/` | Build-tagged, gated on `FOUNDRY_TEST_*`; creates billable Azure resources |
| `docs/` | Starlight docs site |

## Platform Constraints That Bite

These are not preferences — the platform enforces them:

- **linux/amd64 only.** The `Dockerfile` pins `GOARCH=amd64` and CI builds with
  `--platform linux/amd64`. Do not "simplify" that away.
- **Images must be in Azure Container Registry.** Foundry will not pull from
  ghcr.io. The published GHCR image is meant to be mirrored into an ACR.
- **CPU/memory are three fixed pairs**, immutable per version: `0.5`/`1Gi`,
  `1`/`2Gi`, `2`/`4Gi`. They validate as enums, not free strings, so a typo
  fails at validation instead of after the resource exists.
- **`PORT` is injected.** Read it; the documented 8088 is only a local fallback.
- **`AGENT_*` and `FOUNDRY_*` are reserved** environment prefixes. PromptKit's
  `PROMPTPACK_*` namespace is clear of both — no prefixed-duplicate workaround
  is needed here, unlike vertex's `GOOGLE_CLOUD_*` collision.
- **Agent versions are immutable.** Apply creates a version and then PATCHes
  the served-version selector; traffic splitting is not supported (one
  `FixedRatio` rule at 100%).
- **`/readiness` is our job**, but it does not gate deployment. Microsoft's
  Python and C# protocol libraries provide it; a Go container must serve it
  itself. Measured against a live project: an image that pulls but listens on
  nothing still reaches `active` and takes 100% of traffic.
- **`active` means the image pulled, and nothing more.** The platform validates
  the image at version-create — a bad tag fails with "Container image not
  found" — but does not start or probe the container until a session exists.
  A deployment reporting healthy is therefore not evidence that it serves.

## Go Code Standards

- **Cognitive complexity**: below **15** (`gocognit`). Extract helpers proactively.
- **Line length**: max 120 characters (`lll`) — this applies inside the JSON
  Schema string literal too.
- **Magic numbers**: `mnd` flags them in arguments, cases, conditions,
  operations, returns and assignments. Extract named constants.
- **Duplicated strings**: extract literals used 2+ times (`goconst`, min 2).
- **Formatting**: `gofmt` + `goimports`, local prefix
  `github.com/AltairaLabs/promptarena-deploy-foundry`.
- **Errors**: sentinel errors matched with `errors.Is`, never on message text.
- **Test coverage**: changed files need >= 80%. Cover error paths, not just
  happy paths.

## Key Architecture Patterns

### adaptersdk integration

The adapter is a JSON-RPC 2.0 subprocess, not a CLI. `main.go` calls
`adaptersdk.Serve(provider)`. `Provider` implements `GetProviderInfo`,
`ValidateConfig`, `Plan`, `Apply`, `Destroy`, `Status` and `Import`. A
compile-time `var _ deploy.Provider = (*Provider)(nil)` assertion in
`provider.go` turns an SDK signature change into a build failure.

### Capabilities are honest

`GetProviderInfo` advertises only what is implemented. Do not add a capability
string ahead of the operation working end to end.

### Factory injection

Control-plane clients are built through a function field on `Provider` so tests
can substitute a simulated client without interface mocking. Follow vertex's
`clientFunc` shape when the client lands.

### Apply returns state on partial failure

Apply must return the state to persist even when it errors, so resources
already created are never orphaned and in-flight versions reconcile on the next
apply.

## Documentation

`docs/` is a Starlight site and the source of truth for the Foundry deploy
docs. Diagrams use **d2** (`astro-d2`), not mermaid — a ` ```d2 ` block renders
on the site; a ` ```mermaid ` block will not.

Pages are published only through the parent PromptKit docs site
(promptkit.altairalabs.ai), which fetches `docs/src/content/docs/**` from
`main` at build time. A **new** page will not appear there until it is added to
the foundry `extraFiles` map in PromptKit's
`docs/scripts/fetch-adapter-docs.mjs`; edits to already-mapped pages flow
through on the next PromptKit release.

## Pre-commit / CI

CI mirrors `make lint` and `make test`, plus a `docker build --platform
linux/amd64`. Run `make check` before committing. **Never use `--no-verify`.**

## SonarCloud Quality Gate (CI)

Runs on every PR and enforces quality on **new code only**:

| Metric | Threshold |
|--------|-----------|
| Coverage | >= 80% on new/changed lines |
| Duplicated lines | <= 3% |
| Reliability rating | A |
| Security rating | A |
| Maintainability rating | A |

The Sonar job self-skips when `SONAR_TOKEN` is unset, so forks are not blocked.

## Prior Art

| Repo | What to take from it |
|---|---|
| `promptarena-deploy-vertex` | Adapter and runtime structure, plan/state/hash discipline |
| `promptarena-deploy-agentcore` | Full lifecycle with destroy + status, phased apply with progress reporting |
| `promptarena-deploy-omnia` | Credential handling, console output, community repo files |
