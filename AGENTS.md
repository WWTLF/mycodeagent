# AGENTS.md - mycodeagent Development Guidelines

> **⚠️ IMPORTANT:** Agents should NOT call tools (Read, Glob, Bash, etc.) from the "thinking" mode. Always use explicit tool calls when needed. Use thinking for reasoning and planning only.

## Project Overview

`mycodeagent` is a Go CLI tool that deploys and manages vLLM-based coding/writing models on [vast.ai](https://vast.ai). It abstracts the vast.ai API, vLLM startup, SSH tunnel management, and lifecycle operations.

## Layer Architecture

The project follows **DDD / SOLID / Clean Architecture** with strict layering:

```
Application Service    → orchestrates use cases (init, stop, pull)
  └─> App struct with domain repository dependencies
Domain
  └─> Service               → business logic (VLLM deploy lifecycle)
  └─> Entity                → core models (Instance, Model)
  └─> Repository interface   → contracts only
Infrastructure
  ├─> Repository impl       → SQLite (~/.mycodeagent/sqlite/), vast.ai API client
  └─> API impl              → SSH/tunnel operations, HTTP to vast.ai
```

**Domain layer must have zero external imports** (no stdlib Go packages except time).

## Build & Run

```bash
go build -o mycodeagent ./cmd/mycodeagent
./mycodeagent models      # list available models
./mycodeagent init <name> # deploy a model to vast.ai
./mycodeagent ps          # show running instances
./mycodeagent stop <id>   # stop by DB ID
./mycodeagent kill        # stop all instances
```

## Testing

```bash
go test ./...                          # run all tests
go test -v ./internal/domain/...       # run domain package tests only
go test -run TestDeployService/Deploy  # run single test with pattern
go test -count=1 ./...                 # remove parallelization artifacts
go test -race ...                      # race detection
```

## Code Style Guidelines

### Imports - strict order (matches std: go fmt)

```go
import (
    "fmt"                    // standard library
    "time"

    "github.com/WWTLF/mycodeagent/internal/domain/entity"  // named imports
    "github.com/WWTLF/mycodeagent/internal/domain/repository"
    "github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
)
```

**Rules:**
- Standard library first (alphabetical)
- Named 2nd-party imports
- Package imports last (with module path prefixes)
- No blank `ignore` imports needed; unused imports trigger lint errors

### Formatting & Linting

```bash
gofmt -w .       # auto-format all Go files
golangci-lint run --fix
staticcheck ./...
```

Run before committing or pushing: `go build -o mycodeagent ./cmd/mycodeagent && (gofmt -l . | grep -v '^$' || true)`

### Naming Conventions

| Type          | Convention   | Example                  |
|---------------|---------------|---------------------------|
| Packages      | lowercase     | `internal/domain/entity`  |
| Structs       | PascalCase    | `Instance`, `DeployService` |
| Interfaces    | PascalCase    | `VastaiProvider`          |
| Variables     | lower_snake   | `dbPath`, `localPort`     |
| Functions     | verb+Noun     | `OpenDB`, `SearchOffers`  |
| Private funcs | CamelCase (Go convention) / lowercase for single char |

Follow `func OpenDB()` or `func SearchOffers(...)` not `func opendb()`.

Enums are capitalized consts: `StatusRunning Status = "running"`, not "RUNNING".

### Types & Structs

- Prefer structs over interfaces when implementation-specific
- Domain layer uses only **named return values** and `error` type (no panic)
- Interfaces should be minimal: `InstanceRepository interface { ... }` without methods on domain structs that could leak concerns to Infrastructure. 
- Private fields acceptable; prefer explicit field names for clarity

### Error Handling

Always wrap errors at boundaries:

```go
if err := s.vastai.CreateInstance(...); err != nil {
    return nil, fmt.Errorf("create instance: %w", err) // domain-level wrapping
}
```

Don't unwrap errors blindly; propagate context. Never return bare DB or API errors from the domain without wrapping them.

### Comments & Documentation

- Use inline comments only for *non-obvious* logic (not self-explanatory code)
- Struct/func doc blocks follow standard Go godoc format with description + params/returns as needed
- Don't add repetitive `//` lines before each field in type declarations
- Prefer blank line breaks for readability, not excessive comments

### Code Quality Checklist

- [ ] No unused imports
- [ ] All variables initialized before use
- [ ] Short variable names (`dbPath`) acceptable; verbose only when semantics differ from identifier
- [ ] Error wrapping consistently at interface boundaries
- [ ] Constants capitalized; function params and return values follow their respective conventions
- [ ] Use `go generate` for code generation; never commit generated output without comments
- [ ] Private methods prefixed with lowerCamelCase; private with lowercase

## Cursor Rules (`.cursor/rules/`)

Use these custom rules:
- Repository interfaces live in domain layer; implementations belong in infrastructure package
- Domain structs must not import stdlib Go packages except `time` for timestamps
- VLLM image (`vllm/vllm-openai:latest`) is the official default; update only if necessary
- HF_TOKEN applies to gated models (Llama 3, some Mistral variants); Qwen3/2.5, GLM-E are public
- SSH tunnels forward local ports to remote vLLM port 8000; each `init` allocates a new local port for concurrency
- State is persisted in SQLite at ~/.mycodeagent/sqlite

## Copilot Instructions (`.github/copilot-instructions.md`)

See `.github/copilot-instructions.md` for detailed Copilot setup instructions specific to this repository.

## Additional Notes

- Base deployment port defaults to 8000; configurable via env or config file
- Models auto-download from HuggingFace on first launch and cache in `/root/.cache/huggingface/`
- SSH/tunnel operations use standard Go `github.com/pkg/sftp` conventions for connection management
- Always pass `&context.Context{}` when making API calls requiring cancellation or timeout