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
| `image` | string | Yes, unless `runtime_override.image` is set | Azure Container Registry reference to the runtime image. Must be `linux/amd64`. |
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
| `rollout_stage` | enum | No | `live` (default), `candidate` or `promote`. See [Staged rollouts](#staged-rollouts). |
| `dry_run` | boolean | No | Simulate apply without calling Azure. |
| `runtime_override` | object | No | Run another container in place of the runtime. See [Runtime override](#runtime-override). |

## Staging large packs

A pack that serializes to more than `pack_inline_limit_bytes` cannot travel as
an environment variable, so it is uploaded to Blob storage and the agent reads
it from there. Set `staging_container` to the container URL:

```json
"staging_container": "https://myaccount.blob.core.windows.net/promptkit"
```

The blob is named after a hash of the pack, so re-deploying an unchanged pack
uploads nothing.

The credential running the deploy needs a data-plane role on that container —
**Storage Blob Data Contributor** is the usual choice. Access to the Foundry
project does not grant it: without the role the deploy signs in normally and
then fails on the upload.

A pack over the limit with no `staging_container` set is refused at plan time,
before anything is created.

## Staged rollouts

By default an apply creates a version and serves it at once. `rollout_stage`
splits that in two, so a version can be tried before any new session uses it:

| `rollout_stage` | Apply does |
|---|---|
| unset or `live` | Creates a version and points the endpoint at it at 100% traffic. |
| `candidate` | Creates a version and leaves the endpoint where it is. State records it as the **staged** version. |
| `promote` | Creates no version. Checks the staged version against the platform's version list, and that it is `active`, then points the endpoint at it. |

```yaml
deploy:
  provider: foundry
  config:
    # ...
    rollout_stage: candidate   # then: promote
```

A typical rollout applies the pack with `candidate`, tests the staged version by
opening a session on it (see
[Invoke a deployed agent](/how-to/invoke/#a-session-on-a-specific-version)),
then applies the same pack and config with `promote`.

- **Both stages are idempotent.** A candidate for a pack and config that are
  already staged, or already served, creates nothing. A promote whose version
  is already served succeeds and does nothing.
- **Promote serves only what was staged for this pack and config.** A promote
  with a different pack or config is refused, so the state never records hashes
  the served version was not built from. A promote with nothing staged is
  refused too. Neither changes the endpoint.
- **A staged version deleted from the platform** fails the promote and is
  dropped from state, so the next candidate apply stages a new one.
- **`rollout_stage` is not in the config hash.** Moving from `candidate` to
  `promote` is not a config change.
- **A live apply ignores the staged version.** It compares the pack and config
  against the *served* version only, so a pack that is staged but not served
  still creates and serves a version. The staged record is kept.
- The adapter deletes no versions. Neither the served nor the staged version is
  ever removed, short of destroying the agent.

`deploy plan` and `deploy status` list both the served and the staged version.

## Version metadata

Every version the adapter creates carries this metadata alongside your `tags`:

| Key | Value |
|---|---|
| `app.altairalabs.ai/managed-by` | `promptarena` |
| `promptkit.altairalabs.ai/pack-id` | The pack's `id`. |
| `pack_version` | The compiled pack's `version` field. Left out when the pack has none. |

These win over a tag of the same name. They read back from the version list:

```
GET https://{account}.services.ai.azure.com/api/projects/{project}/agents/{agent}/versions?api-version=v1
```

The list is newest first under `data`, each entry with its `version`, `status`
and `metadata`. It is paged: when `has_more` is true, pass `last_id` back as
`after` (`&limit=100&after={last_id}`) for the next page.

## Runtime override

`runtime_override` runs another container in place of the runtime image. The
adapter still creates one agent and still sets the pack environment
(`PROMPTPACK_PACK_JSON` or `PROMPTPACK_PACK_URI`, and the rest of
[the injected environment](#environment-injected-into-the-container)). When
`runtime_override.image` is set it adds `PROMPTPACK_AGENT`, naming the pack's
entry agent, so the container can find the pack and the agent to serve. An
override with no image keeps the bundled runtime and only adds environment.

| Field | Type | Description |
|---|---|---|
| `runtime_override.image` | string | Image to run instead of `image`. Same rules: an Azure Container Registry the project can pull from, `linux/amd64`. |
| `runtime_override.env` | map of string | Added to the agent's environment. Values are stored in the agent definition in plain text. |
| `runtime_override.secret_env` | map of `{name}` | Variables resolved from a Foundry **project connection** when a session's container starts. Only a placeholder is stored in the definition. |
| `runtime_override.command` | list | Refused: a hosted agent runs the image's own entrypoint. |
| `runtime_override.args` | list | Refused, for the same reason. |

A `secret_env` `name` is `<connection>` or `<connection>/<credential-key>`. It
becomes the platform placeholder
`${{connections.<connection>.credentials.<credential-key>}}`, with the key
defaulting to `key`, which is where an `ApiKey` connection keeps its secret. A
`CustomKeys` connection uses the key names it was created with. `version` is
refused: connections are not versioned, and the current value is read every
time a session starts.

```yaml
runtime_override:
  image: myacr.azurecr.io/acme/other-runtime:1.0.0
  env:
    RELAY_URL: wss://relay.example.com/v1/channel
  secret_env:
    RELAY_KEY: { name: relay-secrets/relay_key }
```

Create the connection before deploying. A placeholder that does not resolve
leaves the variable **empty** at start-up rather than failing the deploy.

Validation rejects `command` and `args`; a variable name that is not a portable
environment name, starts with `PROMPTPACK_`, `AGENT_` or `FOUNDRY_`, or is
`PORT` or `OTEL_EXPORTER_OTLP_ENDPOINT`; a name set in both `env` and
`secret_env`; and a `secret_env` entry with no name, a malformed name, or a
`version`. An unknown key inside the block is refused, so a misspelled `image`
cannot quietly deploy the bundled runtime. Plan refuses an override image when
the pack has no entry agent (`agents.entry`, or failing that a single prompt),
because there is then no agent to hand the container.

Changing any of these fields changes the config hash, so the next apply creates
a new agent version.

What the platform does with a replacement container:

- It is started **only when a session is created**, and is sent the declared
  `protocols` on the injected `PORT`. A version reports `active` once its image
  pulls, whatever the container does.
- The platform waits for `GET /readiness` to answer `200` before routing a
  call. A container that does not serve it still runs, but every call to it
  returns `424 session_not_ready`. A container whose work arrives some other
  way, such as over an outbound connection it opens itself, can live with that.
- Its output is readable per session through the `:logstream` route; see
  [Invoke a deployed agent](/how-to/invoke/#troubleshooting).

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

Errors include a missing `account` or `project`, no `image` and no
`runtime_override.image`; missing or
half-specified `cpu`/`memory`; a cpu/memory pair that is not one of the three
legal combinations; an unknown protocol; an
`idle_timeout_minutes` outside 5–60; a `staging_container` that is not an
absolute `https://` URL; an `otlp_endpoint` without a scheme; a duplicated or
sourceless binding; a tag name containing `<>%&\?/` or over the length
limits; and any of the `runtime_override` refusals above.

Warnings include an image outside an Azure Container Registry, a protocol the
bundled runtime does not serve, an `otlp_endpoint` that merely overrides what
the platform injects, no binding named `default`, and, for a runtime override,
what the platform does with a replacement container. The protocol warning is
skipped when `runtime_override.image` is set, since what a replacement serves
is its own business.

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

`PROMPTPACK_AGENT` is never set for the bundled runtime. One Foundry agent
serves the whole pack, and the runtime's precedence — `PROMPTPACK_AGENT`, then
`agents.entry`, then the sole prompt — already means "serve the pack, entry
first" in its absence. With a [runtime override](#runtime-override) image it is
set to the entry agent, because a replacement container is promised one.

Foundry injects its own `FOUNDRY_*` and `AGENT_*` variables, plus `PORT`,
`HOME` and `AZURE`-style identity values. Those prefixes are reserved by the
platform; `PROMPTPACK_*` is clear of both, so no prefixed-duplicate workaround
is needed.
