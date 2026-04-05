# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**mycodeagent** is a Go CLI tool that automates deploying and managing vLLM-based coding/writing models on [vast.ai](https://vast.ai). It wraps the vast.ai API to handle instance creation, vLLM startup, SSH tunnel setup, and lifecycle management.

Key reference: https://docs.vast.ai/api-reference/introduction

## CLI Commands

| Command | Purpose |
|---|---|
| `mycodeagent models` | List available models (coding, fiction writing, dolphin) |
| `mycodeagent init <model>` | Deploy model: autoload from HuggingFace, start vLLM, establish SSH tunnel, save PID to `~/.mycodeagent/sqlite` |
| `mycodeagent ps` | List deployed instances (PID → Model Name) |
| `mycodeagent stop <PID>` | Stop a specific instance |
| `mycodeagent kill` | Kill all instances |
| `mycodeagent pull` | Sync instance list from vast.ai |
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

- Models run via **vLLM** (`vllm/vllm-openai` image) on vast.ai GPU instances
- Models auto-download from HuggingFace on first launch; cached at `/root/.cache/huggingface/`
- HF_TOKEN only needed for gated models (Llama 3, some Mistral); Qwen3, Magnum, GLM are public
- SSH tunnels forward a local port to the remote vLLM port 8000
- Each `init` allocates a new local port to allow multiple concurrent models
- State persisted in `~/.mycodeagent/sqlite`

## Build & Run

```bash
go build -o mycodeagent ./cmd/mycodeagent
go test ./...
go test ./domain/...    # run tests for a single package tree
```
