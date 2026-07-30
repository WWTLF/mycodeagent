# mycodeagent

Rent a GPU on [vast.ai](https://vast.ai), run a coding model on it, and use it from
your editor as if it were local. Destroy it when you're done and pay only for the
minutes you actually used.

```
$ mycodeagent init coder-max
Startup timeout: 30m0s
Searching for 1x GPU with >= 48GB VRAM (host disk >= 50GB)...
Selected: 1x RTX A6000 (48 GB each) at $0.397/hr
Creating instance (disk 38GB)...
Instance running: SSH at ssh7.vast.ai:36010 (provisioned in 1m4s)
Starting SSH tunnel on port 8000...
  downloading model: 13.1 / 25.6 GB (51%) at 57 MB/s
  downloading model: 25.6 / 25.6 GB (100%) at 59 MB/s

API available at: http://localhost:8000/v1

$ mycodeagent kill 80
Instance 80 destroyed.
```

That session cost about **20 cents**. It's an OpenAI-compatible endpoint, so anything
that speaks that protocol works — opencode, Open WebUI, curl, your own script.

## Install

```bash
go install github.com/WWTLF/mycodeagent/cmd/mycodeagent@latest
```

Or grab a binary from [Releases](https://github.com/WWTLF/mycodeagent/releases) —
Linux and macOS, amd64 and arm64:

```bash
tar xzf mycodeagent_v0.1.0_linux_amd64.tar.gz
sudo mv mycodeagent_v0.1.0_linux_amd64 /usr/local/bin/mycodeagent
```

Windows is not built yet: the tunnel uses `Setpgid` and `syscall.Kill`, which don't
exist there. Two functions need build-tagged variants; nothing else is Unix-specific.

You also need an `ssh` client on `PATH` and an SSH keypair in `~/.ssh`.

## First run

```bash
mycodeagent login
```

Asks for your vast.ai API key (from
[console.vast.ai/account](https://console.vast.ai/account/)) and, optionally, a
HuggingFace token — you only need one for gated repos, and nothing in the default
catalog is gated. It also uploads your SSH public key to vast.ai, without which the
tunnel can't be established.

Then check what's on offer:

```bash
$ mycodeagent models
ALIAS       NAME             GPUs      CTX   R  V  T  $/HR    GGUF REPO                     QUANT
coder-mini  qwen35-9b        1x 16GB   64K   +  -  +  $0.069  unsloth/Qwen3.5-9B-GGUF       UD-Q5_K_XL
coder       qwen36-27b-24g   1x 24GB   32K   +  -  +  $0.116  unsloth/Qwen3.6-27B-GGUF      IQ4_XS
coder-fast  qwen36-35b-a3b   1x 24GB   64K   +  -  +  $0.116  unsloth/Qwen3.6-35B-A3B-GGUF  UD-IQ4_XS
coder-hq    qwen36-27b-32g   1x 32GB   64K   +  -  +  $0.202  unsloth/Qwen3.6-27B-GGUF      Q5_K_M
coder-max   qwen36-27b-48g   1x 48GB  128K   +  -  +  $0.376  unsloth/Qwen3.6-27B-GGUF      UD-Q6_K_XL
rude        qwen36-35b-a3b-… 1x 32GB  128K   +  -  +  $0.202  mradermacher/Huihui-…         Q4_K_M
```

`R`/`V`/`T` are reasoning, vision, tool-calling. Prices are live — the cheapest
matching offer at that moment.

## Which model

| Alias | Pick it when |
|---|---|
| `coder-mini` | You want cheap. 9B, fine for small edits and questions. |
| `coder` | Default choice. Full 27B on every token, 32k window. |
| `coder-fast` | You need speed over depth — ~3× the tokens/s, but behaves like a ~10B model. |
| `coder-hq` | Same 27B, better quant, double the window. |
| `coder-max` | Best quality this tool offers. 27B at ~6.5 bits, 128k window. |
| `rude` | Uncensored. Fast MoE, 128k window. |

The short version: **`coder` unless you have a reason.** `coder-max` if quality
matters more than $0.28/hr. `coder-fast` only if you're impatient — it's a mixture-of-
experts model that activates 3B of its 35B per token, so it's quick but noticeably
weaker.

Measured on real deploys: `coder-max` generates ~24 tok/s and processes prompts at
~410 tok/s.

## Everyday use

```bash
mycodeagent init coder          # rent + start + tunnel  (5-10 min)
mycodeagent ps                  # what's running, is it healthy
mycodeagent config              # wire it into opencode
# ... work ...
mycodeagent kill 12             # done, billing stops
```

`ps` is how you find the id for everything else:

```bash
$ mycodeagent ps
ID  VAST ID   STATUS   ALIAS      MODEL           HEALTH   TUNNEL URL
--  -------   ------   -----      -----           ------   ----------
12  46320281  running  coder-max  qwen36-27b-48g  healthy  http://localhost:8000/v1
```

`ID` is the local number every command takes. `VAST ID` is vast.ai's own — only
`tunnel` wants that one.

### Talking to it

The endpoint is plain OpenAI-compatible:

```bash
curl http://localhost:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen36-27b-48g","messages":[{"role":"user","content":"hi"}]}'
```

There's also a **chat UI built into the server** — just open
<http://localhost:8000> in a browser. No setup, and it renders the model's
reasoning correctly, which most third-party clients don't.

Note the models think before answering. Their reasoning arrives in a separate
`reasoning_content` field, so a client that only reads `content` will show a pause
and then a short reply, losing the thinking. opencode and the built-in UI handle it.

### With opencode

```bash
mycodeagent init coder
mycodeagent config
```

`config` asks the running server what it actually serves — the model id and the real
context window — instead of trusting the catalog, then writes a provider block into
`~/.config/opencode/opencode.jsonc`.

It only touches `provider` keys starting with `mycodeagent-`. Your other providers,
`mcp`, `lsp`, `permission`, agents and your default model are left alone — so a
subscription default like `opencode-go/kimi-k2.6` survives. That also means **you have
to pick the new model in opencode yourself**; it won't switch for you.

Run `mycodeagent config` again after `kill` to drop the dead provider.

## Cost

The GPU bills by the second while it exists. Everything else is noise.

```bash
$ mycodeagent budget
ID  MODEL           STATUS   $/HR   HOURS  TOTAL $
--  -----           ------   ----   -----  -------
12  qwen36-27b-48g  running  0.397  0.5    0.21

Total estimated spend: $0.21
```

**`kill` when you stop working.** Not `stop`.

`stop` releases the GPU but keeps the instance, and vast.ai keeps charging for its
disk around the clock — about $0.006/hour for a 28 GB disk. What that buys you is
skipping the next download, which costs roughly $0.004 of GPU time. A single night
idle is $0.14 against $0.004. `stop` never wins on price.

It's still useful for two things: holding a host that turned out to be fast, or
pausing mid-debugging. `start` brings it back on the same machine with the model
already cached.

Ctrl-C during `init` is safe — the instance is destroyed before the process exits.

## When a deploy fails

It happens; vast.ai is a marketplace of other people's machines. The tool destroys
the instance on any failure, so a bad deploy costs pennies, not a forgotten GPU.

**It sits at "Pulling from ghcr.io" and doesn't move.** The host can't fetch the
container image. If the message hasn't changed in 2-3 minutes it won't recover —
Ctrl-C and retry; you'll get a different machine.

**Download crawls at ~1 MB/s.** The host's route to HuggingFace is bad. Some regions
are much worse than others. Restrict where you rent:

```bash
mycodeagent init coder --country RO,PL,DE
```

Codes are ISO-3166 alpha-2; `mycodeagent init --help` lists the ones with machines
behind them. Narrower means pricier, and possibly nothing at all.

**"no GPU offers found".** Your filters are too tight — a rare tier, a narrow
`--country`, or too many blacklisted hosts. Check with `mycodeagent hosts list` and
clear it with `mycodeagent hosts clear`.

**A host failed you twice.** It's already skipped: machines that never boot, never
accept SSH, or burn the whole budget provisioning get recorded automatically.
Interrupting a deploy yourself does *not* blacklist anything.

**The tunnel died but the model is fine.** Reconnect with the vast.ai id:

```bash
mycodeagent tunnel 46320281
```

**Look at the logs.** `mycodeagent log <id>` fetches the instance's bootstrap output.
Note that llama.cpp prints nothing while downloading the model, so silence there is
normal — `init` reports progress separately.

## All commands

| | |
|---|---|
| `login` | Store the vast.ai key + HF token, upload your SSH key |
| `models` | Catalog with live pricing |
| `init <model>` | Rent, start, tunnel. `--country`, `--create-instance-only` |
| `ps` | List instances with health |
| `config` | Write running instances into opencode's config |
| `kill <id>` | Destroy. The normal way to finish. |
| `stop <id>` / `start <id>` | Release the GPU but keep the disk / resume |
| `restart <id>` | Restart the model server on a live instance |
| `tunnel <vastai_id>` | Re-attach a dead tunnel |
| `log <id>` | Instance bootstrap log |
| `budget` | Spend so far and the run rate |
| `hosts` | Inspect / clear the bad-host list |
| `info` | Runtime summary |

`-v` on any command logs every vast.ai API request and response.

## State on your machine

- `~/.mycodeagent/config.yaml` — API key, HF token, base port
- `~/.mycodeagent/mycodeagent.db` — instances and the bad-host list
- `VASTAI_API_KEY` / `HF_TOKEN` override the config file

Nothing is stored on the rented machine that you'd miss: it's destroyed on `kill`,
and the model re-downloads next time.

## How it works, briefly

`init` searches vast.ai for the cheapest verified offer meeting the model's VRAM,
disk and bandwidth requirements, creates an instance running
`ghcr.io/ggml-org/llama.cpp:server-cuda-*`, waits for SSH, opens a local port
forward to the server's port 8000, and waits for it to answer. Every step shares one
deadline; if any of them misses it, the instance is destroyed.

Design notes, the model sizing arithmetic and the llama.cpp flag reference are in
[`docs/Solution.md`](docs/Solution.md). Contributor guide: [`AGENTS.md`](AGENTS.md).

## Build from source

```bash
git clone https://github.com/WWTLF/mycodeagent && cd mycodeagent
make build        # ./mycodeagent
make check        # vet + gofmt + tests
make release      # dist/ tarballs for linux and darwin, with checksums
```

## Licence

MIT — see [LICENSE](LICENSE).
