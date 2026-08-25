---
title: Configuration reference
description: Every field the foundry deploy provider accepts.
---

The full schema is served by `GetProviderInfo`, so `promptarena` validates a
config before the adapter is ever invoked.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `account` | string | Yes | Foundry account name. The data plane host is `{account}.services.ai.azure.com`. |
| `project` | string | Yes | Foundry project the agent is created in. |
| `image` | string | Yes | Azure Container Registry reference to the runtime image. Must be `linux/amd64`. |
| `cpu` | enum | Yes | `"0.5"` \| `"1"` \| `"2"`. Pairs with `memory`; immutable per version. |
| `memory` | enum | Yes | `"1Gi"` \| `"2Gi"` \| `"4Gi"`. Must match the `cpu` value's legal pair. |
| `protocols` | list | No | `invocations`, `invocations_ws`, `responses`. Defaults to `[invocations]`. |
| `idle_timeout_minutes` | integer | No | 5–60. Platform default is 15 when unset. |
| `providers` | list | Yes | Provider bindings. At least one with role `llm`. |
| `observability.tracing_enabled` | boolean | No | Emit OTel traces from the deployed agent. Off by default. |
| `observability.otlp_endpoint` | string | No | Overrides the endpoint Foundry injects. Full URL with scheme. |
| `azure_endpoint` | string | No | Azure OpenAI endpoint for voice. Derived from `account` when unset. |
| `staging_container` | string | No | Azure Blob container URL for packs over the inline limit. |
| `pack_inline_limit_bytes` | integer | No | Size above which the pack is staged. Default 24576. |
| `state_store.kind` | string | No | Where conversation history lives: `memory`, `file` or `redis`. Default `memory`. |
| `state_store.root` | string | No | Directory for `file`. Defaults to a directory under the sandbox `$HOME`. |
| `state_store.url_from_env` | string | No | Environment variable holding the redis connection string. Required for `redis`. |
| `tags` | map | No | Applied as agent metadata. |
| `dry_run` | boolean | No | Simulate apply without calling Azure. |

## Provider bindings

```yaml
providers:
  - name: default
    role: llm
    arena_provider: gpt4o     # or: type + model
```

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Logical name. `default` is treated as primary. |
| `role` | No | `llm` \| `embedding` \| `tts` \| `stt` \| `image` \| `inference`. Defaults to `llm`. |
| `arena_provider` | One of | Inherit type and model from this arena provider id. |
| `type` | One of | Provider family, e.g. `openai`. Not the platform. |
| `model` | With `type` | For Azure, the **deployment name**. |

A binding must name **exactly one** source. Setting both `arena_provider` and
`type`/`model`, or neither, is an error.

### Azure specifics

- **`type` is the family, not the platform.** Use `openai`; `azure` fails with
  `unsupported provider type: azure`.
- **`model` is the deployment name.** Azure OpenAI addresses a deployment, so a
  model deployed as `gpt-4-1-mini` is referenced by that, not `gpt-4.1-mini`.
- **Speech-in should be `whisper`.** The pipeline requests
  `response_format: verbose_json`, which newer transcribe models reject.

## Validation

Structural problems are errors; advisories are warnings and do not make a
config invalid. All problems are reported together rather than one per run.

Errors include a missing `account`, `project` or `image`; missing or
half-specified `cpu`/`memory`; a cpu/memory pair that is not one of the three
legal combinations; an unknown protocol; an
`idle_timeout_minutes` outside 5–60; a `staging_container` that is not an
absolute `https://` URL; an `otlp_endpoint` without a scheme; a duplicated or
sourceless binding; and a tag name containing `<>%&\?/` or over the length
limits.

Warnings include an image outside an Azure Container Registry, a protocol the
bundled runtime does not serve, an `otlp_endpoint` that merely overrides what
the platform injects, and no binding named `default`.

## Environment injected into the container

The adapter sets these; the runtime reads them.

| Variable | Contents |
|---|---|
| `PROMPTPACK_PACK_JSON` | The pack, inline. |
| `PROMPTPACK_PACK_URI` | Blob URI, when the pack is staged instead. |
| `PROMPTPACK_PROVIDERS` | Resolved bindings as JSON. |
| `PROMPTPACK_TOOL_SPECS` | Tool execution config, when the arena declares any. |
| `PROMPTPACK_AZURE_ENDPOINT` | Azure OpenAI endpoint, used by voice. |
| `PROMPTPACK_TRACING_ENABLED` | `true` when tracing is on. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Only when overriding the platform's own. |

`PROMPTPACK_AGENT` is deliberately never set. One Foundry agent serves the whole
pack, and the runtime's precedence — `PROMPTPACK_AGENT`, then `agents.entry`,
then the sole prompt — already means "serve the pack, entry first" in its
absence.

Foundry injects its own `FOUNDRY_*` and `AGENT_*` variables, plus `PORT`,
`HOME` and `AZURE`-style identity values. Those prefixes are reserved by the
platform; `PROMPTPACK_*` is clear of both, so no prefixed-duplicate workaround
is needed.
