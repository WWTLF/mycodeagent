# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**mycodeagent** is a Go CLI tool that automates deploying and managing llama.cpp-based coding/writing models on [vast.ai](https://vast.ai). It wraps the vast.ai API to handle instance creation, `llama-server` startup, SSH tunnel setup, and lifecycle management.

Key reference: https://docs.vast.ai/api-reference/introduction

## CLI Commands

| Command | Purpose |
|---|---|
| `mycodeagent models` | List available models with live cheapest-offer pricing |
| `mycodeagent init <model>` | Deploy model: rent GPU, start `llama-server` (downloads the GGUF from HuggingFace), establish SSH tunnel. Destroys the instance automatically if startup fails. `--country FR,DE` restricts the offer search; `--port 8010` pins the local end of the tunnel. |
| `mycodeagent ps` | Sync with vast.ai and list deployed instances |
| `mycodeagent stop <id>` | Release the GPU, keep the instance + container disk (disk keeps billing) |
| `mycodeagent start <id>` | Resume a stopped instance and reopen its tunnel (`--port` to keep the old one); skips the GGUF download |
| `mycodeagent kill <id>` | Destroy an instance permanently — the cheap way to finish |
| `mycodeagent restart <id>` | Regenerate the startup script and restart the server |
| `mycodeagent tunnel <vastai_id>` | Re-attach an SSH tunnel to a running instance (`--port` to land back on a known URL) |
| `mycodeagent log <id>` | Fetch the vast.ai bootstrap log |
| `mycodeagent budget` | Show consumption by instances |
| `mycodeagent config` | Write all running instances into `~/.config/opencode/opencode.jsonc` |
| `mycodeagent hosts` | Inspect / clear the bad-host blacklist |
| `mycodeagent login` | Store the vast.ai key + HF token, upload the SSH public key |
| `mycodeagent info` | Runtime summary |

## Architecture

Follow **DDD / SOLID / Clean Architecture** layering. The engine is reached only through the `service.EngineProvider` interface — if you find yourself writing a llama.cpp flag, a process name or a log path anywhere outside `internal/infrastructure/engine/`, it belongs behind that interface instead.

```
Application Service    ← orchestrates use cases (init, stop, sync, login, …)
Domain
  ├── Service          ← business logic (deploy lifecycle, tunnels, model resolution)
  ├── Entity           ← Instance, Model, BadHost, Budget
  └── Repository       ← interfaces only
Infrastructure
  ├── Repository impl  ← SQLite (~/.mycodeagent/mycodeagent.db), static model catalog
  └── API impl         ← vast.ai REST calls, SSH/tunnel operations, engine, server probe
```

Dependencies point inward: Infrastructure → Domain ← Application. Domain has zero external imports.

## Infrastructure Details

- **llama.cpp is the default engine** (`ghcr.io/ggml-org/llama.cpp:server-cuda-*` image, pinned by build number in `engine/llamacpp.go`). All *coding* catalog models are GGUF quants pulled with `llama-server -hf <repo>:<quant>`.
- **Three engines, one interface.** `engine.MultiEngine` is the only `service.EngineProvider` the domain sees; it routes on `model.EngineType` (`llamacpp` / `comfyui` / `jupyter`), and an unset type means llama.cpp so pre-existing rows keep working. Adding an engine means obeying three rules — the service port must reach `StartTunnel`, `/proc` patterns must be bracketed so they cannot match their own command *or script filename*, and the onstart script must `exit 1` on failure because vast.ai drops the image `ENTRYPOINT` under `runtype: "ssh"`. See "Three rules a new engine has to obey" in `docs/Solution.md`; `engines_test.go` enforces all three across every engine.
- **The image is a bare CUDA runtime.** `nvidia/cuda:12.8.1-runtime-ubuntu24.04` plus `libgomp1 curl ffmpeg` — **no procps**, so nothing may use `pgrep`/`pkill`; process checks scan `/proc/*/cmdline` instead. The binary lives at `/app/llama-server` and `/app` is **not** on `PATH`, so the onstart script resolves it explicitly.
- **CUDA floor is tied to the image.** The offer search requires `cuda_max_good >= 12.8` because the image is built on CUDA 12.8.1; bumping the image to a newer CUDA base means bumping that filter in `vastai/client.go`.
- **Instances are disposable / pay-as-you-go.** No persistent volumes: the model downloads to the container disk each `init` and is gone when the instance is destroyed. `init` requests a per-model container disk (`Model.DiskGB`) sized to hold the image + download + scratch.
- **Failed deploys self-destroy.** If the server crashes or startup times out, `Deploy` destroys the vast.ai instance and kills the tunnel so a failed `init` never leaves a paid GPU running. A liveness watcher fails fast on a dead `llama-server` instead of waiting out the full timeout; the probe command and log path come from `EngineProvider`, not from the domain layer.
- GGUFs auto-download from HuggingFace on launch; cached at `/root/.cache/huggingface/hub/models--<org>--<repo>/` on the ephemeral container disk (verified on a live deploy — **not** `~/.cache/llama.cpp`). The in-flight blob is a sibling `.downloadInProgress` file. `init` now reports progress from this directory every 20s, because llama.cpp itself prints nothing while downloading.
- HF_TOKEN only needed for gated models; the current catalog (Qwen3.5 / Qwen3.6 quants from unsloth and mradermacher) is public.
- **The catalog is four VRAM tiers — 16 / 24 / 32 / 48 GB — plus explicit MoE entries (`coder-fast` at 24 GB; `coder-fast-max`, `coder-xl` and `coder-glm` at 48 GB) and one uncensored model at 32 GB**, all single-GPU. It is optimised for **capability first**: the working tiers are dense models (all parameters active per token), with MoE kept as explicit named entries rather than a default. Sizing is driven by the KV cache, not the weights — see `docs/Solution.md` for the arithmetic before changing `ContextLength` or `Quant`.
- **Quant tags must match exactly one file in the repo.** `Q6_K` also matches `UD-Q6_K_XL`, and llama.cpp resolves the ambiguity silently. Check any new tag against the repo's file list.
- SSH tunnels forward a local port to the remote `llama-server` port 8000; each `init` takes the next port free from `base_port` (8000), so instances stack up without collision. The port is **reserved (a held socket) before the GPU is rented** and announced as the first line of the run — `--port` pins it instead, and fails immediately if it is busy.
- State persisted in `~/.mycodeagent/mycodeagent.db` (SQLite). Host blacklisting (`bad_hosts`) is applied only for host-side failures, never model-side crashes.

@docs/Solution.md

## Build & Run

```bash
go build -o mycodeagent ./cmd/mycodeagent
go test ./...
go test ./internal/infrastructure/...   # run tests for a single package tree
go vet ./... && gofmt -l .              # pre-commit check
```
