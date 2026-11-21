package foundry

import (
	"encoding/json"

	"github.com/AltairaLabs/promptarena/v2/deploy"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// configSchema is the JSON Schema (draft-07) for the foundry provider config.
// The runtime_override block comes from the deploy SDK, so it reads the same
// in every adapter's schema.
var configSchema = withRuntimeOverride(baseConfigSchema)

// withRuntimeOverride adds the SDK's runtime_override schema to a config
// schema. The input is a constant of this package, so a failure is a
// programming error and panics at start-up, where a test sees it.
func withRuntimeOverride(schema string) string {
	var doc map[string]any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		panic("foundry: config schema is not JSON: " + err.Error())
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		panic("foundry: config schema has no properties")
	}
	props[deploy.RuntimeOverrideConfigKey] = adaptersdk.RuntimeOverrideSchema()
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic("foundry: config schema: " + err.Error())
	}
	return string(out)
}

// baseConfigSchema is the adapter's own part of the schema.
//
// cpu and memory are enums rather than free strings: Foundry offers exactly
// three immutable cpu/memory pairs, and a typo in a free string would only
// surface after the agent version had already been created.
const baseConfigSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["account", "project", "providers"],
  "properties": {
    "account": {
      "type": "string",
      "description": "Foundry account name; the data plane host is {account}.services.ai.azure.com"
    },
    "project": {
      "type": "string",
      "description": "Foundry project the agent is created in"
    },
    "image": {
      "type": "string",
      "description": "ACR reference to the runtime image (linux/amd64); required unless runtime_override.image is set"
    },
    "cpu": {
      "type": "string",
      "enum": ["0.5", "1", "2"],
      "description": "vCPU allocation; pairs with memory as 0.5/1Gi, 1/2Gi, 2/4Gi"
    },
    "memory": {
      "type": "string",
      "enum": ["1Gi", "2Gi", "4Gi"],
      "description": "Memory allocation; must match the cpu value's legal pair"
    },
    "protocols": {
      "type": "array",
      "minItems": 1,
      "description": "Protocol contracts the container serves",
      "items": {
        "type": "string",
        "enum": ["responses", "invocations", "invocations_ws"]
      }
    },
    "idle_timeout_minutes": {
      "type": "integer",
      "minimum": 5,
      "maximum": 60,
      "description": "Session idle timeout before the sandbox is reclaimed (default 15)"
    },
    "azure_endpoint": {
      "type": "string",
      "description": "Azure OpenAI endpoint the deployed agent binds providers against; derived from account when unset"
    },
    "state_store": {
      "type": "object",
      "description": "Where the deployed agent keeps conversation history. Defaults to memory.",
      "properties": {
        "kind": {
          "type": "string",
          "enum": ["memory", "file", "redis"],
          "description": "memory: one container. file: the session sandbox. redis: shared across containers."
        },
        "root": {
          "type": "string",
          "description": "Directory for kind file; defaults to a directory under the sandbox $HOME"
        },
        "url_from_env": {
          "type": "string",
          "description": "Environment variable holding the redis URL, read at deploy time"
        }
      },
      "additionalProperties": false
    },
    "staging_container": {
      "type": "string",
      "description": "Azure Blob container URL for packs too large to inline as environment variables"
    },
    "pack_inline_limit_bytes": {
      "type": "integer",
      "minimum": 1,
      "description": "Serialized pack size above which the pack is staged to Blob storage"
    },
    "tags": {
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "Tags applied to created resources"
    },
    "observability": {
      "type": "object",
      "properties": {
        "tracing_enabled": {
          "type": "boolean",
          "description": "Emit OTel traces, including eval scores, from the deployed agent"
        },
        "otlp_endpoint": {
          "type": "string",
          "description": "Overrides the injected OTEL_EXPORTER_OTLP_ENDPOINT; must be a full URL with scheme"
        }
      },
      "additionalProperties": false
    },
    "rollout_stage": {
      "type": "string",
      "enum": ["live", "candidate", "promote"],
      "description": "live (default) creates and serves a version; candidate creates it unserved; promote serves it"
    },
    "dry_run": {
      "type": "boolean",
      "description": "When true, Apply simulates resource creation without calling Azure"
    },
    "providers": {
      "type": "array",
      "minItems": 1,
      "description": "Provider bindings; the binding named default is primary",
      "items": {
        "type": "object",
        "required": ["name"],
        "properties": {
          "name": { "type": "string" },
          "role": {
            "type": "string",
            "enum": ["llm", "embedding", "tts", "stt", "image", "inference"]
          },
          "arena_provider": {
            "type": "string",
            "description": "Inherit type and model from this arena provider id"
          },
          "type": { "type": "string" },
          "model": {
            "type": "string",
            "description": "For Azure OpenAI this is the deployment name, not the model name"
          }
        },
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false
}`
