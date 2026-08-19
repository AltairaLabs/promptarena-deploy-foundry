# promptarena-deploy-foundry

[![CI](https://github.com/AltairaLabs/promptarena-deploy-foundry/actions/workflows/ci.yml/badge.svg)](https://github.com/AltairaLabs/promptarena-deploy-foundry/actions/workflows/ci.yml)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=AltairaLabs_promptarena-deploy-foundry&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=AltairaLabs_promptarena-deploy-foundry)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=AltairaLabs_promptarena-deploy-foundry&metric=coverage)](https://sonarcloud.io/summary/new_code?id=AltairaLabs_promptarena-deploy-foundry)
[![Go Report Card](https://goreportcard.com/badge/github.com/AltairaLabs/promptarena-deploy-foundry)](https://goreportcard.com/report/github.com/AltairaLabs/promptarena-deploy-foundry)
[![Go Reference](https://pkg.go.dev/badge/github.com/AltairaLabs/promptarena-deploy-foundry.svg)](https://pkg.go.dev/github.com/AltairaLabs/promptarena-deploy-foundry)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

An [Azure AI Foundry](https://ai.azure.com) deploy adapter for
[PromptKit](https://github.com/AltairaLabs/PromptKit). It deploys a PromptKit
pack as a Foundry **hosted agent** — a bring-your-own-container agent on
Microsoft-managed, per-session-isolated infrastructure — and manages its
lifecycle over the Foundry data-plane REST API. It runs as a JSON-RPC 2.0
subprocess that `promptarena` discovers and invokes automatically.

It is the Azure sibling of
[`promptarena-deploy-vertex`](https://github.com/AltairaLabs/promptarena-deploy-vertex)
(Google Agent Runtime) and
[`promptarena-deploy-agentcore`](https://github.com/AltairaLabs/promptarena-deploy-agentcore)
(AWS Bedrock AgentCore).

> **Status: under construction.** The repository scaffolding, CI, release and
> runtime-image pipelines are in place; the lifecycle operations are being
> built out in phases. `GetProviderInfo` reports the capabilities the installed
> version actually supports.

## Why hosted agents

Foundry's *declarative* agents run orchestration server-side, which would mean
the pack's pipeline, guardrails, workflow state machine and eval hooks never
run — packs would behave differently deployed than they do locally. Azure
Container Apps would preserve the pipeline but forgo per-session isolation,
managed identity per agent and the protocol surface.

Hosted agents keep the whole pipeline inside our container while the platform
supplies identity, scaling, session isolation and observability.

## Components

| Component | Purpose |
|---|---|
| `promptarena-deploy-foundry` | JSON-RPC deploy adapter plugin (stdio) |
| `foundry-runtime` | Container entrypoint serving the Foundry protocol contracts |

The runtime image is published to
`ghcr.io/altairalabs/promptkit-foundry-runtime`. Foundry pulls only from Azure
Container Registry, so mirror the tag you want into your own ACR and point
`image` at that.

## Install

The adapter is distributed as a released binary; `promptarena` downloads and
installs it for you:

```bash
promptarena deploy adapter install foundry
```

Pin a version, list installed adapters, or remove one:

```bash
promptarena deploy adapter install foundry@1.0.0
promptarena deploy adapter list
promptarena deploy adapter remove foundry
```

## Configure

Add a `deploy` block to your `arena.yaml`:

```yaml
deploy:
  provider: foundry
  config:
    account: my-foundry-account        # {account}.services.ai.azure.com
    project: my-project
    image: myacr.azurecr.io/altairalabs/promptkit-foundry-runtime:v0.1.0
    cpu: "1"                           # "0.5" | "1" | "2"
    memory: "2Gi"                      # "1Gi" | "2Gi" | "4Gi"
    protocols: [responses, invocations, invocations_ws]
    idle_timeout_minutes: 15           # 5-60
    staging_container: https://acct.blob.core.windows.net/promptkit
    providers:
      - { name: default,    role: llm, arena_provider: gpt4o }
      - { name: speech-in,  role: stt, arena_provider: whisper }
      - { name: speech-out, role: tts, arena_provider: cartesia }
    observability:
      tracing_enabled: true
```

Voice is not a separate config concern: STT and TTS are provider bindings like
any other, and text and voice share one pipeline, so a pack behaves identically
over either.

## Configuration Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `account` | `string` | Yes | Foundry account; the data plane host is `{account}.services.ai.azure.com` |
| `project` | `string` | Yes | Foundry project the agent is created in |
| `image` | `string` | Yes | Azure Container Registry reference to the runtime image (linux/amd64) |
| `cpu` | enum | No | `0.5` \| `1` \| `2` — pairs with `memory`; immutable per version |
| `memory` | enum | No | `1Gi` \| `2Gi` \| `4Gi` — must match the `cpu` value's legal pair |
| `protocols` | list | No | `responses`, `invocations`, `invocations_ws` |
| `idle_timeout_minutes` | `integer` | No | 5–60, default 15 |
| `staging_container` | `string` | No | Azure Blob container URL for packs too large to inline |
| `pack_inline_limit_bytes` | `integer` | No | Serialized pack size above which the pack is staged |
| `providers` | list | Yes | Role-aware bindings (`llm`, `embedding`, `tts`, `stt`, `image`, `inference`) |
| `tags` | `map[string]string` | No | Tags applied to created resources |
| `observability.tracing_enabled` | `boolean` | No | Emit OTel traces from the deployed agent (off by default) |
| `observability.otlp_endpoint` | `string` | No | Overrides the endpoint Foundry injects; full URL with scheme |
| `dry_run` | `boolean` | No | Simulate apply without calling Azure |

For Azure OpenAI, a binding's `model` carries the Azure **deployment name**,
not the model name.

## Protocols

| Protocol | Container serves | Transport | Purpose |
|---|---|---|---|
| `responses` | `POST /responses` | HTTP, platform-managed SSE | Portal playground, OpenAI-compatible clients |
| `invocations` | `POST /invocations` | HTTP + raw SSE | Arbitrary JSON in/out |
| `invocations_ws` | `WS /invocations_ws` | Full-duplex WebSocket | Real-time voice |

`GET /readiness` is required of every hosted agent. Microsoft's Python and C#
protocol libraries provide it; a Go container serves it itself.

## Resource model

One pack maps to **one** Foundry agent with **N immutable versions**. Any apply
where the pack or config hash moves creates a new version, then PATCHes the
served-version selector to it at 100% traffic. Prior versions are recorded, so
a rollback is a selector PATCH rather than an image rebuild.

PromptKit routes between the pack's agents in-process via `runtime/composition`
and `runtime/workflow` — one resource, one endpoint, one Entra identity, no
inter-agent network hops, and routing semantics identical to local execution.

## Platform constraints

- **linux/amd64 only.** arm64 images are rejected.
- The image must live in an Azure Container Registry.
- CPU/memory are three fixed pairs, immutable per version.
- Scaling is per session, not per replica.
- The platform injects `PORT` (8088), plus the `FOUNDRY_*` and `AGENT_*`
  namespaces. PromptKit's `PROMPTPACK_*` namespace is clear of both.

## Development

### Prerequisites

- Go 1.26+
- [golangci-lint](https://golangci-lint.run/usage/install/)
- [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports) (`go install golang.org/x/tools/cmd/goimports@latest`)
- Docker, for `make docker-build`

```bash
make install-hooks   # point core.hooksPath at .githooks
make build           # adapter + runtime binaries
make test            # tests with the race detector
make lint            # golangci-lint
make check           # fmt + lint + test + build
make docker-build    # linux/amd64 runtime image
```

Deployed integration tests create billable Azure resources and are gated behind
the `integration` build tag — see [`test/integration/README.md`](test/integration/README.md).

## License

MIT — see [LICENSE](LICENSE).

## Links

- [PromptKit](https://github.com/AltairaLabs/PromptKit)
- [PromptKit deploy docs](https://promptkit.altairalabs.ai) (`arena/how-to/deploy`)
- Adapter docs: [`docs/`](docs/src/content/docs/)
