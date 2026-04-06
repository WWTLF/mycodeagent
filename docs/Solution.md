# Solution: Instance Deployment Logic

## Overview

`mycodeagent init <model>` deploys a model on a vast.ai GPU instance and exposes it as an OpenAI-compatible API on localhost. Models can run via **vLLM** or **LM Studio** engines. The entire startup is governed by a single `context.Context` with a per-model timeout (default 10 min, up to 20 min for multi-GPU models).

## Architecture Layers

```mermaid
graph TD
    CLI["CLI Command<br/><code>mycodeagent init</code>"]
    APP["Application Service"]
    DS["DeployService<br/><i>domain/service</i>"]
    EP["EngineProvider<br/><i>interface</i>"]
    VP["VastaiProvider<br/><i>interface</i>"]
    SP["SSHTunnelProvider<br/><i>interface</i>"]
    VE["VLLMEngine<br/><i>infrastructure</i>"]
    LE["LMStudioEngine<br/><i>infrastructure</i>"]
    VA["Vastai Adapter<br/><i>infrastructure</i>"]
    SA["SSH Adapter<br/><i>infrastructure</i>"]
    VAST["vast.ai API"]
    SSH["SSH / Tunnel"]
    DB["SQLite<br/><code>~/.mycodeagent/mycodeagent.db</code>"]

    CLI --> APP --> DS
    DS --> EP
    DS --> VP
    DS --> SP
    EP -.-> VE
    EP -.-> LE
    VP -.-> VA --> VAST
    SP -.-> SA --> SSH
    DS --> DB

    style DS fill:#2d6a4f,color:#fff
    style EP fill:#40916c,color:#fff
    style VP fill:#40916c,color:#fff
    style SP fill:#40916c,color:#fff
```

Dependencies point inward: Infrastructure implements Domain interfaces. Domain has zero external imports.

### Engine Provider Pattern

The `EngineProvider` interface abstracts engine-specific deployment details:

```go
type EngineProvider interface {
    DockerImage() string
    BuildOnstart(model *entity.Model, hfToken string) string
    BuildRawCommand(model *entity.Model) string
    VolumeMountPath() string
    RestartCommands(model *entity.Model) (killCmd string, startCmd string)
}
```

Two implementations:

| Engine | Docker Image | Model Format | Server Port |
|---|---|---|---|
| **VLLMEngine** | `vllm/vllm-openai:v0.19.0` | HuggingFace (AWQ/FP16) | 8000 |
| **LMStudioEngine** | `nvidia/cuda:12.4.1-runtime-ubuntu22.04` | GGUF via `lms get` | 8000 |

## Deploy Flow

```mermaid
sequenceDiagram
    participant User
    participant CLI
    participant DeployService
    participant EngineProvider
    participant VastaiProvider
    participant SSHTunnelProvider
    participant VastAI as vast.ai API
    participant Remote as GPU Instance

    User->>CLI: mycodeagent init <model>
    CLI->>DeployService: Deploy(modelName)

    Note over DeployService: Create context with<br/>model.StartupTimeout

    DeployService->>DeployService: Resolve model profile
    DeployService->>EngineProvider: BuildOnstart(model, hfToken)
    EngineProvider-->>DeployService: onstart script
    DeployService->>VastaiProvider: SearchOffers(VRAM, numGPUs)
    VastaiProvider->>VastAI: GET /bundles (search, filter deverified)
    VastAI-->>VastaiProvider: Sorted offers
    VastaiProvider-->>DeployService: Cheapest offer

    DeployService->>DeployService: ensureVolume(offer, mountPath)
    DeployService->>VastaiProvider: CreateInstance(offerID, image, env, onstart, volumeID)
    VastaiProvider->>VastAI: PUT /asks/{id}
    VastAI-->>VastaiProvider: instanceID
    VastaiProvider-->>DeployService: instanceID

    rect rgb(50, 50, 80)
        Note over DeployService,Remote: All waits share one context (timeout)
        DeployService->>VastaiProvider: WaitForInstance(ctx, instanceID, volumeID)
        loop Poll every 10s until "running"
            VastaiProvider->>VastAI: GET /instances/{id}
            Note over VastaiProvider: Check: deverified? stopped?<br/>volume still exists?
            VastAI-->>VastaiProvider: status + volume check
        end
        VastaiProvider-->>DeployService: sshHost, sshPort, rate

        DeployService->>SSHTunnelProvider: WaitForSSH(ctx, host, port)
        loop Dial TCP every 5s
            SSHTunnelProvider->>Remote: TCP connect
        end
        SSHTunnelProvider-->>DeployService: SSH ready

        DeployService->>SSHTunnelProvider: FindFreePort(basePort)
        SSHTunnelProvider-->>DeployService: localPort

        DeployService->>SSHTunnelProvider: StartTunnel(localPort, host, port)
        SSHTunnelProvider-->>DeployService: tunnelPID

        DeployService->>SSHTunnelProvider: WaitForHealth(ctx, localPort)
        Note over SSHTunnelProvider: Runs in goroutine
        loop GET /v1/models every 10s
            SSHTunnelProvider->>Remote: HTTP localhost:localPort/v1/models
        end
        SSHTunnelProvider-->>DeployService: Healthy (or ctx timeout)
    end

    DeployService->>DeployService: Save instance to SQLite
    DeployService-->>CLI: Instance
    CLI-->>User: API available at localhost:{port}/v1
```

## Timeout Strategy

A single `context.WithTimeout` wraps the entire startup sequence. Each wait phase consumes from the same deadline -- if an earlier phase is slow, later phases have less time, and the whole flow fails cleanly on expiry.

```mermaid
graph LR
    subgraph "context.WithTimeout(model.StartupTimeout)"
        A["WaitForInstance<br/>poll 10s<br/>+ volume check"] --> B["WaitForSSH<br/>poll 5s"]
        B --> C["StartTunnel"]
        C --> D["WaitForHealth<br/>poll /v1/models 10s<br/><i>goroutine</i>"]
    end

    T["ctx.Done()"] -.->|cancels| A
    T -.->|cancels| B
    T -.->|cancels| D

    style T fill:#d00,color:#fff
```

### Per-Model Timeouts

| Model | Alias | Engine | GPUs | Timeout |
|---|---|---|---|---|
| `qwen3-5-35b-a3b-awq` | coder | vLLM | 2x GPU (24 GB) | 20 min |
| `qwen3-vl-32b-instruct-awq` | coder_vl | vLLM | 2x GPU | 15 min |
| `qwen25-32b-instruct-awq` | writer | vLLM | 2x GPU | 15 min |
| `dolphin-glm-24b` | rude | vLLM | 2x GPU | 15 min |
| `qwen25-coder-32b-gguf` | coder-2 | LM Studio | 1x GPU (24 GB) | 15 min |

Bigger models (multi-GPU, larger downloads) get longer timeouts. If `StartupTimeout` is unset, the default is **10 minutes**.

## Volume Management

Volumes provide persistent storage for model caching across instance restarts.

```mermaid
flowchart TD
    INIT["init <model>"] --> CHECK{"Volume exists<br/>for this machine?"}
    CHECK -->|Yes| ATTACH["Attach existing volume"]
    CHECK -->|No| CREATE["RentVolume via API"]
    CREATE --> PARSE["Parse ID from name<br/>V.123456 → 123456"]
    PARSE --> SAVE["Save to SQLite"]
    SAVE --> ATTACH
    ATTACH --> INSTANCE["Create instance<br/>with volume_info"]

    INSTANCE --> POLL["WaitForInstance loop"]
    POLL --> VOLCHECK{"Volume still<br/>exists? (API)"}
    VOLCHECK -->|Yes| CONTINUE["Continue polling"]
    VOLCHECK -->|No| DESTROY["Destroy instance<br/>+ clean up DB"]

    KILL["kill <id>"] --> KILLINST["Destroy instance"]
    KILLINST --> KILLVOL["Delete volume from vast.ai + DB"]

    style DESTROY fill:#d00,color:#fff
    style KILLVOL fill:#d00,color:#fff
```

- Volumes are created per-machine and tracked in SQLite (`volume_id` on instance)
- `volume list` calls `GET /api/v0/volumes/` and removes stale local records
- `kill` destroys both the instance and its associated volume
- During `WaitForInstance`, volume existence is verified on each poll; if gone, instance is destroyed

## Health Check

The health check polls `GET /v1/models` (works for both vLLM and LM Studio) in a background goroutine:

```mermaid
flowchart TD
    START["Tunnel established"] --> SPAWN["go WaitForHealth(ctx)"]
    SPAWN --> SELECT{"select"}
    SELECT -->|healthCh returns nil| OK["Instance ready"]
    SELECT -->|healthCh returns error| FAIL["Return error"]
    SELECT -->|ctx.Done| TIMEOUT["Startup timed out"]

    style OK fill:#2d6a4f,color:#fff
    style FAIL fill:#d00,color:#fff
    style TIMEOUT fill:#d00,color:#fff
```

## Instance Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Searching: init <model>
    Searching --> Creating: Offer found (deverified filtered)
    Creating --> EnsureVolume: Create or reuse volume
    EnsureVolume --> WaitingInstance: Instance created
    WaitingInstance --> WaitingSSH: Status = running + volume ok
    WaitingInstance --> Failed: Deverified / stopped / volume gone
    WaitingSSH --> Tunneling: SSH reachable
    Tunneling --> WaitingHealth: Tunnel established
    WaitingHealth --> Running: /v1/models 200 OK
    Running --> Stopped: stop <id>
    Running --> Destroyed: kill <id>
    WaitingInstance --> Failed: Timeout
    WaitingSSH --> Failed: Timeout
    WaitingHealth --> Failed: Timeout
    Stopped --> [*]
    Destroyed --> [*]
    Failed --> [*]
```

## SSH Tunnel Detail

```mermaid
graph LR
    CLIENT["localhost:{localPort}"] -->|SSH -L| TUNNEL["SSH Tunnel<br/>PID tracked"]
    TUNNEL -->|port forward| REMOTE["GPU Instance<br/>:8000"]
    REMOTE --- SERVER["Model Server<br/>vLLM or LM Studio<br/>OpenAI-compatible"]

    style TUNNEL fill:#40916c,color:#fff
```

- Each `init` allocates a new local port (starting from `basePort`, scanning +100)
- Tunnel runs as a background `ssh -N -L` process; PID saved to SQLite
- `stop` sends SIGTERM to the tunnel PID, then stops the vast.ai instance
- `kill` destroys the instance, its tunnel, and associated volume
- `restart` regenerates the onstart script on the instance and restarts the server
