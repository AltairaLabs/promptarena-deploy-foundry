---
title: Configure the adapter
description: Set up a deploy block targeting Azure AI Foundry hosted agents.
---

Add a `deploy` block to your `arena.yaml`. The adapter is selected by the
`provider` value.

```yaml
deploy:
  provider: foundry
  config:
    account: my-foundry-account
    project: my-project
    image: myacr.azurecr.io/altairalabs/promptkit-foundry-runtime:v0.1.0
    cpu: "1"
    memory: "2Gi"
    protocols: [invocations]
    providers:
      - { name: default, role: llm, arena_provider: gpt4o }
```

That is the whole minimum. Three fields are required — `account`, `project` and
`image` — plus at least one provider binding with the `llm` role.

## Account and project

Both are genuine Azure resources. The data plane the adapter talks to is
literally `{account}.services.ai.azure.com/api/projects/{project}`, so a typo
in either is caught before anything is created:

```
project "typo" does not exist in account "my-account"; check the deploy config
```

The project must belong to a **project-enabled** Foundry account
(`allowProjectManagement: true`). An ordinary multi-service Cognitive Services
account cannot host agents.

## The image

Foundry pulls the runtime image, and two constraints are absolute:

- It must be **`linux/amd64`**. arm64 images are rejected when the version is
  created.
- It must live in an **Azure Container Registry**. Foundry will not pull from
  ghcr.io or Docker Hub.

The published image is `ghcr.io/altairalabs/promptkit-foundry-runtime`. Mirror
the tag you want into your own ACR and point `image` at that:

```bash
az acr import --name myacr \
  --source ghcr.io/altairalabs/promptkit-foundry-runtime:v0.1.0 \
  --image altairalabs/promptkit-foundry-runtime:v0.1.0
```

The project's managed identity needs `Container Registry Repository Reader` (or
`AcrPull`) on that registry, or the version fails to provision with
`Container image not found`.

## Sizing

`cpu` and `memory` are required, and they are enums rather than free strings.
Foundry has no default sizing — a deploy without them is rejected outright — and
it offers exactly three pairs, immutable once a version exists:

| cpu | memory |
|---|---|
| `"0.5"` | `"1Gi"` |
| `"1"` | `"2Gi"` |
| `"2"` | `"4Gi"` |

They are validated as pairs, so a mismatch fails at `plan` rather than after
the resource has been created:

```
cpu 1 pairs with memory 2Gi, not "4Gi"; the pairs are fixed and immutable per version
```

Scaling is **per session**, not per replica. These values describe one session's
sandbox, and billing follows cpu plus memory across all active sessions — so
oversizing multiplies cost by your concurrency rather than adding to it once.

## Protocols

`protocols` declares which contracts the endpoint exposes. It defaults to
`[invocations]`, which is what the bundled runtime serves.

| Protocol | What it is |
|---|---|
| `invocations` | Arbitrary JSON in and out, with SSE for streaming |
| `invocations_ws` | Full-duplex WebSocket, used for voice |
| `responses` | OpenAI-compatible; **not yet served by the bundled runtime** |

Declaring a protocol the container does not implement produces an agent the
platform will route to and get nothing back from, so `validate` warns about it.

## Provider bindings

Bindings map a logical name to a concrete provider. The binding named `default`
is the primary; without one, the first `llm`-role binding is used and `validate`
tells you which.

```yaml
providers:
  - { name: default, role: llm, arena_provider: gpt4o }
  - { name: fast,    role: llm, type: openai, model: gpt-4-1-mini }
```

Each binding names **exactly one** source: either `arena_provider` (inherit type
and model from an arena provider) or `type` plus `model` inline.

Two Azure-specific points:

- `type` is the provider **family** (`openai`), not the platform. Setting it to
  `azure` fails with `unsupported provider type: azure`.
- `model` is the Azure **deployment name**, not the model name. If you deployed
  `gpt-4.1-mini` as `gpt-4-1-mini`, use `gpt-4-1-mini`.

## Voice

Voice is not a separate concern — speech in and speech out are bindings like any
other:

```yaml
providers:
  - { name: default,    role: llm, type: openai, model: gpt-4-1-mini }
  - { name: speech-in,  role: stt, type: openai, model: whisper }
  - { name: speech-out, role: tts, type: openai, model: gpt-4o-mini-tts }
protocols: [invocations, invocations_ws]
```

Both speech roles are required for voice; one alone leaves a caller talking to
something that cannot answer.

Use **`whisper`** for speech-in. The newer transcribe models reject the
`verbose_json` response format the pipeline asks for, and the turn fails
mid-flight with `unsupported_value`.

Because text and voice share one pipeline, a pack behaves identically over
either.

## Observability

```yaml
observability:
  tracing_enabled: true
```

That is enough. Foundry injects `OTEL_EXPORTER_OTLP_ENDPOINT` and an
Application Insights connection string into the container, so no endpoint is
required — `otlp_endpoint` only overrides what the platform provides, and must
be a full URL including scheme.

Tracing is off by default: an unconfigured deployment sends nothing and pays
nothing.

## Optional fields

```yaml
idle_timeout_minutes: 15   # 5-60; compute is released after this, $HOME persists
tags: { team: platform }   # stored as agent metadata
dry_run: false             # simulate apply without calling Azure
azure_endpoint: ""         # override the derived Azure OpenAI endpoint
```

See the [configuration reference](../reference/configuration) for every field.
