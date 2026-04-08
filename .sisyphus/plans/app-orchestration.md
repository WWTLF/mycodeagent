# Application Layer Orchestration Plan

## Overview

Refactor `mycodeagent` CLI to implement a proper **Application Layer** where `App` orchestrates use cases by calling domain services — never accessing repositories or infrastructure clients directly.

---

## Current State

### Problem
- `App` struct is a DI container holding repository interfaces only
- CLI commands have mixed access patterns:
  - Some commands call `App` directly (accessing repos)
  - Others call services directly (bypassing App)
- No true application layer orchestration
- Breaches layering principle: **App → Services only, never direct repos/clients**

### Affected Files
- `internal/application/app.go` — Current state: DI container
- `cmd/mycodeagent/main.go` — Dependency injection
- `cmd/mycodeagent/commands/*.go` — 13 commands with inconsistent access patterns

---

## Target Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    CLI Commands                            │
│  (init, stop, kill, restart, tunnel, log, budget, models,  │
│   ps, config, info, login, volume)                         │
└──────────────────────┬──────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────┐
│              Application Layer (App)                       │
│  • Orchestrates use cases                                   │
│  • Calls domain services (NEVER direct repos/clients)      │
│  • Coordinates multi-service workflows                      │
│  • Adds error context where needed                          │
└──────────────────────┬──────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────┐
│                   Domain Services                           │
│  • DeployService (instance lifecycle)                      │
│  • VolumeService (volume management)                       │
└──────────────────────┬──────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────┐
│             Repository Interfaces (Domain)                  │
│  • InstanceRepository                                       │
│  • ModelRepository                                          │
│  • VolumeRepository                                         │
└──────────────────────┬──────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────┐
│            Infrastructure Implementations                   │
│  • SQLite persistence                                       │
│  • vast.ai API client                                       │
│  • SSH/ tunnel operations                                   │
└─────────────────────────────────────────────────────────────┘
```

---

## Scope

### Commands Requiring Refactoring (13 total)

| Command | Current Access | New Pattern | Complexity |
|---------|----------------|-------------|------------|
| `init` | `DeployService` | `App.Deploy()` | Medium |
| `stop` | `DeployService` | `App.Stop()` | Low |
| `kill` | `DeployService` | `App.Kill()` | Low |
| `restart` | `DeployService` | `App.Restart()` | Low |
| `tunnel` | `App` (repo) | `App.StartTunnel()` | High |
| `log` | `App` (repo) | `App.GetLogs()` | Medium |
| `budget` | `App` (repo) | `App.GetBudget()` | Medium |
| `models` | `App.ModelsRepo` | `App.ListModels()` | Low |
| `ps` | `App.InstancesRepo` | `App.ListInstances()` | Low |
| `config` | `App` (config) | `App.GetConfig()` | Low |
| `info` | `App` (mixed) | `App.GetInfo()` | Medium |
| `login` | (no App) | `App.Login()` | Low |
| `volume` | `VolumeService` | `App.VolumeCmd()` | Medium |

---

## Design Decisions

### 1. App Structure

```go
type App struct {
    DeploySvc    *service.DeployService
    VolumeSvc    *service.VolumeService
    Config       *config.Config
}
```

### 2. Orchestration Patterns

**Single Service Call (pass-through):**
```go
func (app *App) DeployModel(name string) error {
    ctx := context.Background()
    return app.DeploySvc.Deploy(ctx, name)  // Always accepts context
}
```

**Multi-Service Coordination (business logic in App):**
```go
func (app *App) DeployWithVolume(ctx context.Context, name string) error {
    vol, err := app.VolumeSvc.Create(ctx, 50)  // Step 1
    if err != nil {
        return fmt.Errorf("create volume: %w", err)
    }
    
    inst, err := app.DeploySvc.Deploy(ctx, name)  // Step 2
    if err != nil {
        // Rollback: delete volume if deployment fails
        app.VolumeSvc.Delete(ctx, vol.ID)
        return fmt.Errorf("deploy with volume: %w", err)
    }
    
    return nil
}
```

**Thin Services for Operations:**
```go
// internal/domain/service/instance_service.go
type InstanceService struct {
    instances repository.InstanceRepository
    ssh       SSHTunnelProvider
    vastai    VastaiProvider
}

// Tunnel operations
func (s *InstanceService) StartTunnel(ctx context.Context, instanceID int64, port int) (int, error)
func (s *InstanceService) StopTunnel(ctx context.Context, instanceID int64) error
func (s *InstanceService) GetLogs(ctx context.Context, instanceID int64) ([]byte, error)

// Budget operations
func (s *InstanceService) GetBudget(ctx context.Context) (*BudgetSummary, error)
```

### 3. Error Handling

- Services return fully-formed errors with context
- App can wrap errors for application-level context
- App handles rollback/compensation logic for multi-service operations

### 4. Dependency Injection

Created in `main.go` and injected:
```go
deploySvc := service.NewDeployService(...)
volumeSvc := service.NewVolumeService(...)

app := &application.App{
    DeploySvc: deploySvc,
    VolumeSvc: volumeSvc,
    Config:    cfg,
}

rootCmd.AddCommand(
    commands.NewInitCmd(app),
    commands.NewStopCmd(app),
    // ...
)
```

---

## Refactoring Strategy

### Phase 1: Service Interface Alignment

**Goal:** Ensure services are designed for App orchestration

**Tasks:**
1. Update `DeployService` method signatures to accept `context.Context`
   - `Deploy(ctx, name)` instead of `Deploy(name)`
   - `DeployCreateOnly(ctx, name)` instead of `DeployCreateOnly(name)`
   - `Stop(ctx, id)` instead of `Stop(id)`
   - `Destroy(ctx, id)` instead of `Destroy(id)`
   - `Restart(ctx, id)` instead of `Restart(id)`

2. Create `InstanceService` for tunnel/log/budget operations
   - `instance_service.go` with methods:
     - `StartTunnel(ctx, instanceID, port) (pid int, err error)`
     - `StopTunnel(ctx, instanceID) error`
     - `GetLogs(ctx, instanceID) ([]byte, error)`
     - `GetBudget(ctx) (*BudgetSummary, error)`
   - Inject: `InstanceRepository`, `SSHTunnelProvider`, `VastaiProvider`

3. Update `VolumeService` method signatures to accept `context.Context`
   - `Create(ctx, sizeGB)` instead of `Create(sizeGB)`
   - `List(ctx) ([]*entity.Volume, error)` instead of `List()`
   - `Delete(ctx, id)` instead of `Delete(id)`

4. Define service method contracts:
   - Input: `context.Context`, domain entities, primitives
   - Output: domain entities, errors (wrapped at service boundary)
   - No infrastructure leaks (services call interfaces only)

**Estimate:** 3-4 hours (includes creating InstanceService + context updates)

### Phase 2: App Layer Implementation

**Goal:** Create orchestrated App methods

**Tasks:**
1. Rewrite `internal/application/app.go`
   ```go
   type App struct {
       DeploySvc *service.DeployService
       VolumeSvc *service.VolumeService
       Config    *config.Config
   }
   
   func NewApp(deploySvc, volumeSvc *service.DeployService, 
                 volumeSvc *service.VolumeService,
                 cfg *config.Config) *App
   // Add orchestration methods...
   ```
2. Implement App methods for each command interface
   - Match existing command method signatures for smooth integration
   - Add orchestration logic for multi-service workflows
3. Add error handling and rollback logic

**Estimate:** 3-4 hours

### Phase 3: Command Layer Refactoring

**Goal:** All commands route through App

**Tasks:**
1. Update command constructors to accept `*App` instead of services/repos
   ```go
   // Before
   commands.NewInitCmd(deploySvc)
   
   // After
   commands.NewInitCmd(app)
   ```
2. Refactor each command's `RunE` to call App methods:
   ```go
   cmd.RunE = func(cmd *cobra.Command, args []string) error {
       return app.DeployModel(args[0])
   }
   ```
3. Update all 13 command files:
   - init.go, stop.go, kill.go, restart.go, tunnel.go, log.go, budget.go
   - models.go, ps.go, config.go, info.go, login.go, volume.go

**Estimate:** 4-5 hours

### Phase 4: `main.go` Integration

**Goal:** Wire up dependencies in application entry point

**Tasks:**
1. Update service creation
2. Create App instance
3. Pass App to all commands

**Estimate:** 1 hour

---

## Files to Modify

| File | Change Type | Estimate |
|------|-------------|----------|
| `internal/application/app.go` | Rewrite | 2h |
| `cmd/mycodeagent/main.go` | Update injection | 0.5h |
| `cmd/mycodeagent/commands/init.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/stop.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/kill.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/restart.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/tunnel.go` | Route through App | 1h |
| `cmd/mycodeagent/commands/log.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/budget.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/models.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/ps.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/config.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/info.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/login.go` | Route through App | 0.5h |
| `cmd/mycodeagent/commands/volume.go` | Route through App | 1h |
| `internal/domain/service/deploy_service.go` | Interface review | 1h |
| `internal/domain/service/volume_service.go` | Interface review | 1h |

**Total Estimate:** 12-14 hours

---

## Guardrails (Must Not Do)

- ❌ **App must NEVER call repository interfaces directly**
- ❌ **App must NEVER call infrastructure clients (vastai, ssh adapters)**
- ❌ **App must NEVER bypass services for business logic**
- ❌ **No new features - only architectural refactoring**
- ❌ **Preserve existing CLI interface contracts (flags, args, output format)**

---

## Acceptance Criteria

1. **All 13 commands** route through `App` (verified by code review)
2. **App only calls services** (verified by static analysis - no App.*Repo or App.*Client calls)
3. **Layering preserved**: Domain layer unchanged, only App layer modified
4. **Build passes**: `go build ./...` and `go test ./...`
5. **Backward compatible**: CLI interface unchanged (flags, args, output)
6. **Service integration improved**: Services designed for App orchestration

---

## Rollback Plan

If refactoring breaks existing functionality:
1. Revert to current `App` implementation (just a DI container)
2. Keep services untouched
3. Commands continue calling services directly or use `App.repo` pattern

---

## Testing Strategy

**No new tests required** (scope is refactoring, not feature addition)

**Verification:**
- `go build ./cmd/mycodeagent` — Build passes
- `./mycodeagent --help` — CLI interface unchanged
- Test each command manually:
  - `./mycodeagent models`
  - `./mycodeagent init <model>`
  - `./mycodeagent ps`
  - `./mycodeagent stop <id>`
  - etc.

---

## Dependencies

**None** — Self-contained refactoring. No external changes required.

**Pre-requisite:** Ensure current codebase builds and passes tests before starting.

---

## Success Metrics

- **Code quality**: Cleaner layering, App is orchestrator not just DI container
- **Maintainability**: Easier to reason about use cases (all orchestration in App)
- **Testability**: Services can be tested in isolation, App tests can mock services
