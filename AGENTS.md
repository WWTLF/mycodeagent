# AGENTS.md - mycodeagent Development Guidelines

> **⚠️ IMPORTANT:** Agents should NOT call tools (Read, Glob, Bash, etc.) from the "thinking" mode. Always use explicit tool calls when needed. Use thinking for reasoning and planning only.

## Project Overview

`mycodeagent` is a Go CLI tool that deploys and manages vLLM-based coding/writing models on [vast.ai](https://vast.ai). It abstracts the vast.ai API, vLLM startup, SSH tunnel management, and lifecycle operations. Instances are disposable (pay-as-you-go) — there are no persistent volumes.

## Layer Architecture

The project follows **DDD / SOLID / Clean Architecture** with strict layering:

```
Application Service    → orchestrates use cases (init, stop, pull)
  └─> App struct with domain repository dependencies
Domain
  └─> Service               → business logic (instance deploy lifecycle)
  └─> Entity                → core models (Instance, Model)
  └─> Repository interface   → contracts only
Infrastructure
  ├─> Repository impl       → SQLite (~/.mycodeagent/mycodeagent.db), vast.ai API client
  └─> API impl              → SSH/tunnel operations, HTTP to vast.ai
```

**Critical layering rule (enforced by code review):**
- `commands → application.App → domain/service → domain/repository`
- `application` must **NEVER** import any package under `internal/infrastructure/`
- `application` must **NEVER** call repository methods directly
- Domain layer must not depend on infrastructure packages or external SDK clients

## Build & Run

```bash
make build              # go build -o mycodeagent ./cmd/mycodeagent
make run                # go run ./cmd/mycodeagent
make test               # go test ./...
make clean              # rm -f mycodeagent
```

```bash
./mycodeagent models            # list available models
./mycodeagent init <name>       # deploy a model to vast.ai
./mycodeagent ps                # show running instances
./mycodeagent stop <id>         # stop by DB ID (keeps instance)
./mycodeagent kill <id>         # destroy an instance permanently
./mycodeagent restart <id>      # restart the model server
./mycodeagent budget            # show consumption by instances
./mycodeagent tunnel <vastai_id># re-attach SSH tunnel
./mycodeagent log <id>          # show vast.ai bootstrap logs
./mycodeagent hosts             # manage bad hosts blacklist
```

## Testing

**There are currently no `*_test.go` files in this repository.**

When adding tests:
```bash
go test ./...                          # run all tests
go test -v ./internal/domain/...       # run domain package tests only
go test -run TestDeployService/Deploy  # run single test with pattern
go test -count=1 ./...                 # remove parallelization artifacts
go test -race ./...                    # race detection
```

## Code Style

### Imports
Standard Go ordering (enforced by `gofmt`):
```go
import (
    "fmt"                    // standard library
    "time"

    "github.com/WWTLF/mycodeagent/internal/domain/entity"  // named imports
    "github.com/WWTLF/mycodeagent/internal/domain/repository"
    "github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
)
```

### Formatting
```bash
gofmt -w .       # auto-format all Go files
```

Pre-commit check: `go build -o mycodeagent ./cmd/mycodeagent && (gofmt -l . | grep -v '^$' || true)`

**Note:** No `golangci-lint` or `staticcheck` configuration files exist in the repo.

### Naming Conventions

| Type          | Convention   | Example                  |
|---------------|--------------|--------------------------|
| Packages      | lowercase    | `internal/domain/entity` |
| Structs       | PascalCase   | `Instance`, `DeployService` |
| Interfaces    | PascalCase   | `VastaiProvider`         |
| Variables     | lowerCamel   | `dbPath`, `localPort`    |
| Functions     | verb+Noun    | `OpenDB`, `SearchOffers` |
| Enums         | Capitalized  | `StatusRunning Status = "running"` |

### Error Handling
Always wrap errors at domain boundaries:
```go
if err := s.vastai.CreateInstance(...); err != nil {
    return nil, fmt.Errorf("create instance: %w", err)
}
```
Never return bare DB or API errors from the domain without wrapping them.

## Configuration & State

- **Config file:** `~/.mycodeagent/config.yaml` (YAML format)
- **Env overrides:** `VASTAI_API_KEY`, `HF_TOKEN`
- **SQLite DB:** `~/.mycodeagent/mycodeagent.db` (auto-migrated on open)
- **Default base port:** 8000 (configurable via `base_port` in config.yaml)

### Config struct
```yaml
vastai_api_key: "your-key"
hf_token: "your-token"
base_port: 8000
```

## Operational Notes

- **VLLM image:** `vllm/vllm-openai:v0.19.0` is the only engine image. All catalog models are AWQ/FP8 quants (never GGUF).
- **HF_TOKEN:** Required only for gated models. The current catalog (Qwen, dolphin) is public.
- **SSH tunnels:** Forward local ports to remote vLLM port 8000. Each `init` allocates a new local port for concurrency.
- **Storage:** No persistent volumes. The HF cache lives on the ephemeral container disk (sized per model via `Model.DiskGB`) and is discarded on destroy.
- **Disposable instances:** A failed `init` (crash or timeout) auto-destroys the vast.ai instance + tunnel so nothing keeps billing. A liveness watcher detects a dead `vllm serve` and fails fast.
- **SSH/tunnel operations:** Shell out to `ssh` and manage lifecycle via process PID + TCP/HTTP checks
- **Context usage:** Always pass a real `context.Context` (typically `context.WithTimeout` or `context.WithCancel`), never `&context.Context{}`

## Database Schema

Auto-migrated tables in SQLite:
- `instances` - deployed instances with tunnel PID, local port, status, GPU count
- `bad_hosts` - blacklisted machine IDs (host-side failures only) skipped during offer selection

## Key Files

- **Entry point:** `cmd/mycodeagent/main.go`
- **Application layer:** `internal/application/app.go`
- **Domain services:** `internal/domain/service/`
- **Domain entities:** `internal/domain/entity/`
- **Repository interfaces:** `internal/domain/repository/`
- **Repository implementations:** `internal/infrastructure/persistence/`
- **vast.ai client:** `internal/infrastructure/vastai/client.go`
- **SSH tunnel:** `internal/infrastructure/ssh/tunnel.go`
- **Engine:** `internal/infrastructure/engine/vllm.go` (the only engine)
- **Config loading:** `internal/infrastructure/config/config.go`

## Commands

All CLI commands live in `cmd/mycodeagent/commands/` and are wired in `main.go`:
- `login` - configure API keys
- `models` - list model catalog with pricing
- `init` - deploy and tunnel
- `ps` - list/sync instances
- `stop` - stop instance (keeps it)
- `kill` - destroy an instance permanently
- `budget` - spending breakdown
- `tunnel` - re-establish SSH tunnel
- `log` - fetch bootstrap logs
- `info` - runtime info
- `config` - write opencode config
- `restart` - restart model server
- `hosts` - bad host management
