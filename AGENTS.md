# AGENTS.md - mycodeagent Development Guidelines

> **⚠️ IMPORTANT:** Agents should NOT call tools (Read, Glob, Bash, etc.) from the "thinking" mode. Always use explicit tool calls when needed. Use thinking for reasoning and planning only.

## Project Overview

`mycodeagent` is a Go CLI tool that deploys and manages llama.cpp-based coding models on [vast.ai](https://vast.ai). It abstracts the vast.ai API, `llama-server` startup, SSH tunnel management, and lifecycle operations. Instances are disposable (pay-as-you-go) — there are no persistent volumes.

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
./mycodeagent stop <id>         # release GPU, keep instance + disk (disk still bills)
./mycodeagent start <id>        # resume a stopped instance, reopen the tunnel
./mycodeagent kill <id>         # destroy an instance permanently
./mycodeagent restart <id>      # restart the model server
./mycodeagent budget            # show consumption by instances
./mycodeagent tunnel <vastai_id># re-attach SSH tunnel
./mycodeagent log <id>          # show vast.ai bootstrap logs
./mycodeagent hosts             # manage bad hosts blacklist
```

## Testing

```bash
go test ./...                              # run all tests
go test -v ./internal/infrastructure/...   # run one package tree
go test -run TestServeArgs ./...           # single test by pattern
go test -count=1 ./...                     # bypass the test cache
go test -race ./...                        # race detection
```

Tests are a *contract* suite, not a coverage exercise — each case locks in a property
whose regression would only surface as money quietly leaking on vast.ai.

`internal/domain/service/` — `fakes_test.go` provides in-memory doubles for every
provider and repository, so the failure paths (which only run when something has
already gone wrong, and so are never exercised by hand) are testable:

| Test | Property locked in |
|---|---|
| `TestDeployCleansUpLocalRowWhenHealthCheckFails` | a failed deploy leaves no instance, no tunnel **and no DB row** |
| `TestDeployDestroysInstanceOnContextCancel` | Ctrl-C tears the billing instance down instead of orphaning it |
| `TestDeployCleansUpWhenInstanceNeverStarts` | teardown before the row exists doesn't try to delete row 0 |
| `TestDeploySuccessKeepsInstanceAndPersistsRuntimeShape` | the happy path destroys nothing; scaled context + GPU count are persisted |
| `TestRestartReusesPersistedContextLength` | restart re-emits the deployed window, not the catalog baseline |
| `TestStartRefreshesSSHAndReopensTunnel` | resume re-reads the SSH endpoint vast.ai reassigned |
| `TestStartDoesNotDestroyOnFailure` | a failed resume never throws away an instance the user chose to keep |
| `TestSyncDedupeKeepsTheRowWithTheTunnel` | dedupe keeps exactly one row — never deletes its own winner |
| `TestSyncDropsRowsWhoseRemoteIsGone` | stale rows are removed and their tunnels killed |
| `TestGetBudgetCountsStatusesCarryingADetailSuffix` | `"running (msg)"` still counts toward the totals |

`internal/infrastructure/engine/llamacpp_test.go`:

| Test | Property locked in |
|---|---|
| `TestServeArgsInjectsRuntimeValues` | `-hf` / `--alias` / `--host` / `--port` / `-ngl` / `--ctx-size` are all emitted |
| `TestServeArgsOmitsQuantWhenUnset` | bare repo ref when `Quant == ""`; no `--ctx-size` when context is 0 |
| `TestServeArgsStripsEngineOwnedFlagsFromCatalog` | a catalog row cannot hijack an engine-owned flag |
| `TestServeArgsSplitMode` | `--split-mode layer` only for multi-GPU, and never on top of a catalog `-sm` |
| `TestBuildOnstartShapeSupportsRestartRewrite` | the `" && bash "` separator `Restart` splits on stays intact |
| `TestBuildOnstartEscapesSingleQuotes` | catalog args with `'` survive the `echo '...'` wrapper byte-for-byte |
| `TestProcessCommandsAvoidProcpsAndSelfMatch` | no `pgrep`/`pkill` (absent from the image); the `llama-serve[r]` pattern never self-matches |

When adding a test here, check it has teeth: reintroduce the bug it targets and
confirm the test goes red. A test that passes against the broken code documents
nothing.

Untested and worth covering next: `EstablishTunnel`, the `vastai.Adapter` mapping
layer, and `serverprobe`'s `/props` fallback.

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

- **Engine image:** `ghcr.io/ggml-org/llama.cpp:server-cuda-b<build>` is the only engine image, pinned in `engine/llamacpp.go`. All catalog models are GGUF quants pulled with `llama-server -hf <repo>:<quant>` (never AWQ/FP8).
- **The image is a bare CUDA runtime** (`nvidia/cuda:12.8.1-runtime-ubuntu24.04` + `libgomp1 curl ffmpeg`). Two consequences that bite:
  - **No procps.** Never write `pgrep`/`pkill`/`ps` into a remote command — scan `/proc/*/cmdline` instead, with the process name bracketed (`llama-serve[r]`) so the command cannot match its own shell.
  - **`/app` is not on `PATH`.** The onstart script resolves the binary itself and `cd`s beside it (the CUDA build uses `GGML_BACKEND_DL`, so ggml backend `.so`s must be in the working directory).
- **CUDA floor is tied to the image.** `SearchOffers` filters `cuda_max_good >= 12.8` because the image is CUDA 12.8.1. Bumping the image to a newer CUDA base means bumping that filter in `vastai/client.go`, or deploys land on hosts whose driver cannot run the runtime.
- **Engine details stay behind `service.EngineProvider`.** A llama.cpp flag, process name, or log path anywhere outside `internal/infrastructure/engine/` is a layering violation.
- **HF_TOKEN:** Required only for gated models. The current catalog (unsloth / mradermacher Qwen3.5 + Qwen3.6 quants) is public.
- **SSH tunnels:** Forward local ports to the remote `llama-server` port 8000. Each `init` allocates a new local port for concurrency.
- **Storage:** No persistent volumes. The GGUF cache lives on the ephemeral container disk (sized per model via `Model.DiskGB`) and is discarded on destroy.
- **Disposable instances:** A failed `init` (crash or timeout) auto-destroys the vast.ai instance + tunnel so nothing keeps billing. A liveness watcher detects a dead `llama-server` and fails fast instead of waiting out the timeout.
- **SSH/tunnel operations:** Shell out to `ssh` and manage lifecycle via process PID + TCP/HTTP checks.
- **Context usage:** Always pass a real `context.Context` (typically `context.WithTimeout` or `context.WithCancel`), never `&context.Context{}`.

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
- **Engine:** `internal/infrastructure/engine/llamacpp.go` (the only engine) + `llamacpp_test.go`
- **Model catalog:** `internal/infrastructure/persistence/model_repo_static.go` (in-memory, no DB)
- **Config loading:** `internal/infrastructure/config/config.go`

## Commands

All CLI commands live in `cmd/mycodeagent/commands/` and are wired in `main.go`:
- `login` - configure API keys
- `models` - list model catalog with pricing
- `init` - deploy and tunnel
- `ps` - list/sync instances
- `stop` - release the GPU, keep instance + disk
- `start` - resume a stopped instance and reopen its tunnel
- `kill` - destroy an instance permanently
- `budget` - spending breakdown
- `tunnel` - re-establish SSH tunnel
- `log` - fetch bootstrap logs
- `info` - runtime info
- `config` - write opencode config
- `restart` - restart model server
- `hosts` - bad host management
