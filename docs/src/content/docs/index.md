---
title: Azure AI Foundry Deploy Adapter
description: Deploy PromptKit packs to Azure AI Foundry hosted agents.
---

`promptarena-deploy-foundry` deploys a PromptKit pack to an **Azure AI Foundry
hosted agent** — a bring-your-own-container agent running on Microsoft-managed,
per-session-isolated infrastructure.

It ships as two binaries:

| Component | Purpose |
|---|---|
| `promptarena-deploy-foundry` | JSON-RPC deploy adapter plugin, launched over stdio by `promptarena` |
| `foundry-runtime` | Container entrypoint serving the Foundry protocol contracts |

The container runs the whole PromptKit pipeline — prompts, guardrails, workflow
state, tools and eval hooks — so a pack behaves the same deployed as it does
locally. Foundry supplies identity, scaling, session isolation and
observability around it.

## Status

The adapter is under active development. Lifecycle operations are being built
out in phases; `promptarena deploy adapter install foundry` will report the
capabilities the installed version actually supports.

## Platform constraints worth knowing up front

- **linux/amd64 only.** Foundry rejects arm64 images.
- The image must live in an **Azure Container Registry**.
- CPU and memory come as three fixed, immutable pairs: `0.5 vCPU / 1 GiB`,
  `1 vCPU / 2 GiB`, `2 vCPU / 4 GiB`.
- Scaling is **per session**, not per replica — each session gets its own
  VM-isolated sandbox.
- Agent versions are immutable. A change to the pack or the config creates a
  new version, and the served version is then selected at 100% traffic.
