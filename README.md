# mycodeagent

A Go CLI that rents a GPU on [vast.ai](https://vast.ai), starts a coding model on it,
and exposes it as an OpenAI-compatible API on `localhost` — then destroys the GPU when
you're done, so you pay only for the hours you actually code.

```
mycodeagent init coder     →  rent GPU → pull GGUF → start llama-server → SSH tunnel
                           →  http://localhost:8000/v1
mycodeagent kill 12        →  instance destroyed, billing stops
```

**llama.cpp is the only engine.** Every catalog model is a GGUF quant served by
`llama-server` from the `ghcr.io/ggml-org/llama.cpp:server-cuda-*` image. That choice
is what puts each model on a *single* GPU, which roughly halves the hourly rate versus
the FP8/AWQ + vLLM setup this replaced. The reasoning, and what it costs, is in
[`docs/Solution.md`](docs/Solution.md#engine-tradeoffs).

**Instances are disposable.** There are no persistent volumes — the model re-downloads
to the ephemeral container disk on every `init`, and a failed startup tears the instance
down automatically so a broken deploy never leaves a paid GPU running.

## Install

```bash
go build -o mycodeagent ./cmd/mycodeagent
./mycodeagent login          # stores the vast.ai API key (+ optional HF token)
```

`login` also uploads your SSH public key to the vast.ai account if it isn't registered
yet — without it the tunnel can't be established.

## Commands

| Command | What it does |
|---|---|
| `mycodeagent models` | List the catalog with live cheapest-offer pricing |
| `mycodeagent init <model>` | Rent a GPU, start `llama-server`, open the SSH tunnel. Self-destroys on failure. |
| `mycodeagent init <model> --create-instance-only` | Create the instance and print SSH details; no tunnel, no health wait |
| `mycodeagent ps` | Sync with vast.ai and list instances (status, health, tunnel URL) |
| `mycodeagent stop <id>` | Release the GPU but keep the instance + disk (see the cost note below) |
| `mycodeagent start <id>` | Resume a stopped instance and reopen its tunnel — skips the download |
| `mycodeagent kill <id>` | Destroy permanently. **This is the one you want when you're done.** |
| `mycodeagent restart <id>` | Regenerate the startup script on the instance and restart the server |
| `mycodeagent tunnel <vastai_id>` | Re-attach an SSH tunnel to an already-running instance |
| `mycodeagent log <id>` | Fetch the vast.ai bootstrap log |
| `mycodeagent budget` | Spend per instance plus hourly / daily / monthly totals |
| `mycodeagent config` | Write every running instance into `~/.config/opencode/opencode.jsonc` |
| `mycodeagent hosts` | Inspect / clear the bad-host blacklist |
| `mycodeagent info` | Runtime summary |

`<id>` is the local DB id shown by `ps`, not the vast.ai id.

Add `-v` to any command to log every vast.ai API request and response.

### `stop` vs `kill`

Both release the GPU. The difference is the **container disk**, which vast.ai bills
around the clock (~$0.15/GB/month) for as long as the instance exists:

| | `stop` → `start` | `kill` → `init` |
|---|---|---|
| Instance on vast.ai | kept, state `stopped` | destroyed |
| Container disk | keeps billing (~$0.006/h for 28 GB) | freed |
| GGUF cache | preserved — resume skips the download | gone, re-downloaded (~2 min) |
| Same physical host | yes | no, picks the cheapest offer again |

Those two minutes of download cost about **$0.004** of GPU time, while a day of
idle disk costs **$0.14**. So `stop` never wins on price — not even overnight.
Reach for it when you want to *hold a specific host* or pause mid-debugging, and
use `kill` when you're done for the day.

Ctrl-C during `init` is safe: the instance is torn down before the process exits.

## Model catalog

Four VRAM tiers, all single-GPU, capability-first: the working tiers are **dense**
models (all parameters active per token) rather than the faster MoE, which is kept as
one explicit opt-in entry.

| Alias | Model | VRAM | Context | Note |
|---|---|---|---|---|
| `coder-mini` | Qwen3.5-9B | 16 GB | 64k | cheapest usable tier |
| `coder` | Qwen3.6-27B `IQ4_XS` | 24 GB | 32k | the default |
| `coder-fast` | Qwen3.6-35B-A3B | 24 GB | 64k | ~3× faster, ~10B-equivalent capability |
| `coder-hq` | Qwen3.6-27B `Q5_K_M` | 32 GB | 64k | one quant step up |
| `coder-max` | Qwen3.6-27B `UD-Q6_K_XL` | 48 GB | 128k | effectively lossless |
| `rude` | Qwen3.6-35B-A3B abliterated | 32 GB | 128k | uncensored |

The context in that table is the *baseline*; `init` scales it up when the rented offer
has more VRAM per GPU than the tier minimum. Sizing is driven by the KV cache, not the
weights — read [`docs/Solution.md`](docs/Solution.md#sizing) before changing
`ContextLength` or `Quant` on any entry.

## Using it with opencode

```bash
mycodeagent init coder
mycodeagent config          # writes the provider block + sets the default model
```

`config` asks each running server what it is actually serving (model id and real
`n_ctx`) rather than trusting the static catalog, so the context limit written into
opencode always matches what the GPU can really take.

## Architecture

DDD / Clean Architecture, dependencies pointing inward:

```
commands → application.App → domain/service → domain/repository
                                   ↓
                      infrastructure (adapters)
```

`internal/domain` has zero infrastructure imports; everything external (vast.ai REST,
SSH, the engine, SQLite) sits behind an interface defined in `internal/domain/service`.
Full layer diagram, deploy sequence, failure handling and the llama.cpp parameter
reference: [`docs/Solution.md`](docs/Solution.md).
Contributor guidelines: [`AGENTS.md`](AGENTS.md).

## State

- `~/.mycodeagent/config.yaml` — API key, HF token, base port
- `~/.mycodeagent/mycodeagent.db` — SQLite: instances + bad-host blacklist
- Env overrides: `VASTAI_API_KEY`, `HF_TOKEN`

## Build

```bash
make build      # go build -o mycodeagent ./cmd/mycodeagent
make test       # go test ./...
```
