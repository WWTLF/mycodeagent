# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**mycodeagent** is a Go CLI tool that automates deploying and managing vLLM-based coding/writing models on [vast.ai](https://vast.ai). It wraps the vast.ai API to handle instance creation, vLLM startup, SSH tunnel setup, and lifecycle management.

Key reference: https://docs.vast.ai/api-reference/introduction

## CLI Commands

| Command | Purpose |
|---|---|
| `mycodeagent models` | List available models with live cheapest-offer pricing |
| `mycodeagent init <model>` | Deploy model: rent GPU, start vLLM (downloads from HuggingFace), establish SSH tunnel. Destroys the instance automatically if startup fails. |
| `mycodeagent ps` | Sync with vast.ai and list deployed instances |
| `mycodeagent stop <id>` | Stop an instance (keeps the vast.ai instance; can restart) |
| `mycodeagent kill <id>` | Destroy an instance permanently |
| `mycodeagent restart <id>` | Regenerate the startup script and restart the server |
| `mycodeagent tunnel <vastai_id>` | Re-attach an SSH tunnel to a running instance |
| `mycodeagent log <id>` | Fetch the vast.ai bootstrap log |
| `mycodeagent budget` | Show consumption by instances |

## Architecture

Follow **DDD / SOLID / Clean Architecture** layering:

```
Application Service    ← orchestrates use cases (init, stop, pull, etc.)
Domain
  ├── Service          ← business logic (tunnel management, model resolution)
  ├── Entity           ← Instance, Model, Tunnel, Budget
  └── Repository       ← interfaces only
Infrastructure
  ├── Repository impl  ← SQLite (~/.mycodeagent/sqlite), vast.ai API client
  └── API impl         ← vast.ai REST calls, SSH/tunnel operations
```

Dependencies point inward: Infrastructure → Domain ← Application. Domain has zero external imports.

## Infrastructure Details

- **vLLM is the only engine** (`vllm/vllm-openai` image). All catalog models are vLLM-servable quants (AWQ or FP8) — never GGUF.
- **Instances are disposable / pay-as-you-go.** No persistent volumes: the model downloads to the container disk each `init` and is gone when the instance is destroyed. `init` requests a per-model container disk (`Model.DiskGB`) sized to hold the download + scratch.
- **Failed deploys self-destroy.** If the server crashes or startup times out, `Deploy` destroys the vast.ai instance and kills the tunnel so a failed `init` never leaves a paid GPU running. A liveness watcher fails fast on a crashed `vllm serve` instead of waiting out the full timeout.
- Models auto-download from HuggingFace on launch; cached at `/root/.cache/huggingface/` (on the ephemeral container disk).
- HF_TOKEN only needed for gated models; the current catalog (Qwen, dolphin) is public.
- SSH tunnels forward a local port to the remote vLLM port 8000; each `init` allocates a new local port for concurrency.
- State persisted in `~/.mycodeagent/mycodeagent.db` (SQLite). Host blacklisting (`bad_hosts`) is applied only for host-side failures, never model-side crashes.

@docs/Solution.md

## Build & Run

```bash
go build -o mycodeagent ./cmd/mycodeagent
go test ./...
go test ./domain/...    # run tests for a single package tree
```
