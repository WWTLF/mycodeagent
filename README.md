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

Coding models are what it's for, but the same rent-use-destroy lifecycle runs two
other things: **ComfyUI** for image generation and **JupyterLab** for notebooks. See
[Images and notebooks](#images-and-notebooks).

## Install

```bash
go install github.com/WWTLF/mycodeagent/cmd/mycodeagent@latest
```

Or grab a binary from [Releases](https://github.com/WWTLF/mycodeagent/releases) —
Linux and macOS, amd64 and arm64:

```bash
tar xzf mycodeagent_v0.2.0_linux_amd64.tar.gz
sudo mv mycodeagent_v0.2.0_linux_amd64 /usr/local/bin/mycodeagent
```

Windows is not built yet: the tunnel uses `Setpgid` and `syscall.Kill`, which don't
exist there. Two functions need build-tagged variants; nothing else is Unix-specific.

You also need an `ssh` client on `PATH` and an SSH keypair in `~/.ssh`. `rsync` is
needed too if you use ComfyUI or Jupyter — it is what copies your work back.

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
ALIAS         NAME                        GPUs     CTX   R  V  T  $/HR    GGUF REPO                     QUANT
comfyui       comfyui                     1x 48GB  -     -  -  -  $0.402                                -
jupyter       jupyter-pytorch             1x 32GB  -     -  -  -  $0.202                                -
jupyter-mini  jupyter-pytorch-16g         1x 16GB  -     -  -  -  $0.072                                -
coder-mini    qwen35-9b                   1x 16GB  64K   +  -  +  $0.067  unsloth/Qwen3.5-9B-GGUF       UD-Q5_K_XL
coder         qwen36-27b-24g              1x 24GB  32K   +  -  +  $0.116  unsloth/Qwen3.6-27B-GGUF      IQ4_XS
coder-fast    qwen36-35b-a3b              1x 24GB  64K   +  -  +  $0.116  unsloth/Qwen3.6-35B-A3B-GGUF  UD-IQ4_XS
coder-hq      qwen36-27b-32g              1x 32GB  64K   +  -  +  $0.202  unsloth/Qwen3.6-27B-GGUF      Q5_K_M
coder-max     qwen36-27b-48g              1x 48GB  128K  +  -  +  $0.402  unsloth/Qwen3.6-27B-GGUF      UD-Q6_K_XL
rude          qwen36-35b-a3b-abliterated  1x 32GB  128K  +  -  +  $0.202  mradermacher/Huihui-…         Q4_K_M
```

`R`/`V`/`T` are reasoning, vision, tool-calling. Prices are live — the cheapest
matching offer at that moment, so they move around; the numbers above were real when
this was written. The top three aren't language models, so most columns don't apply.

## Which model

| Alias | Pick it when |
|---|---|
| `coder-mini` | You want cheap. 9B, fine for small edits and questions. |
| `coder` | Default choice. Full 27B on every token, 32k window. |
| `coder-fast` | You need speed over depth — ~3× the tokens/s, but behaves like a ~10B model. |
| `coder-hq` | Same 27B, better quant, double the window. |
| `coder-max` | Best quality this tool offers. 27B at ~6.5 bits, 128k window. |
| `rude` | Uncensored. Fast MoE, 128k window. |
| `comfyui` | Not a language model — image generation. |
| `jupyter` | Not a language model — a GPU notebook. 32 GB. |
| `jupyter-mini` | The same notebook on the cheapest card. 16 GB — enough for a 7B in 4-bit, a small fine-tune, or ordinary dataframe work. |

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

`config` only writes providers for the language models. `comfyui` and `jupyter`
aren't OpenAI endpoints, so they're skipped rather than written as providers
opencode couldn't use.

## Images and notebooks

Two catalog entries aren't language models. Same commands, same lifecycle, same
billing — what changes is that you open a web UI instead of pointing a client at an
API.

```bash
mycodeagent init comfyui      # ~$0.39/hr, 48 GB card
mycodeagent init jupyter      # ~$0.30/hr, 32 GB card
mycodeagent init jupyter-mini # ~$0.07/hr, 16 GB card
```

Both open in a browser at the URL `mycodeagent ps` prints — `http://localhost:8000`
unless that port was taken, in which case `init` picks the next free one. There's no
password on either: the SSH tunnel is the access control, the port is never exposed
publicly, and a login prompt on a single-user tunnel is just a way to lock yourself
out.

### Getting your work back

This is the part that matters, because **`kill` destroys the disk**. A rsync loop
runs while the instance lives and mirrors into a `COMFY_SYNC/` directory in your
current working directory, every 60 seconds:

```
$ mycodeagent init comfyui
...
Syncing output + workflows to /home/you/project/COMFY_SYNC every 60s
```

| Directory | Direction | Why |
|---|---|---|
| `COMFY_SYNC/output` | down only | Generated images. The instance is their only author. |
| `COMFY_SYNC/workflows` | **both ways** | Workflows are source files. Edit them locally and they reach the instance; save one in the UI and it lands here. |
| `COMFY_SYNC/workspace` | **both ways** | Jupyter's `/workspace` — notebooks and data. Notebooks are source files too: they survive the instance and seed the next one. |

Two-way means your work survives the instance and seeds the next one: `init`
uploads what's already there before pulling anything down.

Nothing is ever deleted, in either direction — a stale local copy must not be able
to erase work on the instance, and vice versa. The cost is that deleting a file on
one side only brings it back on the next pass; delete it on both to make it stick.
When the same file changed on both sides, the newer one wins and the older is left
alone rather than overwritten.

Models are **not** synced back. They're tens of gigabytes and came from the internet
in the first place — re-fetching costs less than uploading over a home connection.

If the tunnel dies, `mycodeagent tunnel <vastai_id>` restarts the sync along with it.

**Choosing where it lands.** `COMFY_SYNC/` in the working directory is awkward when
that directory is a source repository. `--sync-folder` picks somewhere else:

```bash
mycodeagent init jupyter --sync-folder ~/notebooks
# notebooks appear in ~/notebooks/workspace/
```

The path is resolved to an absolute one and stored on the instance, so a later
`tunnel` or `start` run from a different directory still syncs to the same place —
without it, the root was re-derived from whatever directory you happened to be in,
and one instance's files could end up split across two.

Engines that write nothing ignore the flag; the language models never sync.

### Getting models onto a ComfyUI instance

The instance is disposable, so anything you want on it has to arrive at boot. Two
ways:

**A provisioning script.** `--provisioning` takes a URL that the instance downloads
and runs before ComfyUI starts, so checkpoints are in place when the UI opens:

```bash
mycodeagent init comfyui \
  --provisioning https://raw.githubusercontent.com/WWTLF/mycodeagent/main/config/provisioning/photoreal.sh
```

That one is in this repo at [`config/provisioning/photoreal.sh`](config/provisioning/photoreal.sh).
It holds a catalogue of 15 models and does two things with it: downloads a core set
of four (~14 GB — RealVisXL V5.0, its Lightning variant, the fp16-fix SDXL VAE and
the 4x-UltraSharp upscaler), and **registers all fifteen with ComfyUI-Manager** so
the rest show up in its Model Manager and install with one click, on demand.
Registration is a JSON entry rather than a download, which is why the catalogue can
be long while the boot stays short. Add a line to the array to add a model.

Your `HF_TOKEN` and `CIVITAI_TOKEN` (set once with `mycodeagent login`) are in the
script's environment, so it can reach gated repos. **The script runs on a rented
machine with those tokens available — point `--provisioning` only at a URL you
control.**

**ComfyUI-Manager**, which ships in the image, for everything else — custom nodes,
and models from its own catalogue. Anything it fetches lives only until `kill`, and
it isn't synced back.

### Why you can't just paste a link

ComfyUI-Manager will not install an arbitrary URL, and no setting changes that. Its
install endpoint calls `check_whitelist_for_model()` first, which matches the request
against `model-list.json` on `(save_path, base, filename)`; anything absent is
refused with *Invalid model install request*. The check runs before the security
level is consulted, so relaxing that doesn't help.

There are two supported ways in, and the provisioning script uses both:

- **Add it to the catalogue** in your provisioning script. It then appears in the
  Manager, installs on click, and comes back on every future instance.
- **Download it straight onto a running instance**, when you have a link and don't
  want to redeploy:

  ```bash
  ssh -p <ssh_port> root@<ssh_host> \
    'cd /opt/workspace-internal/ComfyUI/models/checkpoints && \
     curl -fL -H "Authorization: Bearer $CIVITAI_TOKEN" -o name.safetensors "<url>"'
  ```

  `mycodeagent ps` shows the instance; the SSH host and port are in `mycodeagent
  info`. Hit refresh in ComfyUI afterwards and it appears in **Load Checkpoint**.

The script also sets the Manager's database to `local`, because that is what makes
the added entries *visible* — the install path reads the local list, the UI does not
unless told to. The trade-off is that the Manager's node and model lists stop
tracking upstream between image rebuilds; switch its DB back to "channel" in the
Manager settings if you want the newest catalogue. Models stay installable either
way.

One caveat when installing through the Manager: its downloader reads the whole file
into memory before writing it, so a 7 GB checkpoint wants 7 GB of free RAM. It also
sends no `Authorization` header, so gated repos (FLUX among them) can only be
fetched by the provisioning script, which does.

### First generation

The image ships Stable Diffusion 1.5, so a bare `init comfyui` can generate
immediately. With `photoreal.sh` you get SDXL models instead, which need two changes
from ComfyUI's default workflow: pick the checkpoint in **Load Checkpoint**, and set
**Empty Latent Image** to 1024×1024 — the 512×512 default is SD1.5's native size and
SDXL produces mush at it.

`RealVisXL_V5.0_fp16` wants ~28 steps at CFG 4.5. The `_Lightning_` variant wants
4-6 steps at CFG 1.5 — draft on it, finish on the full model.

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

For `comfyui` and `jupyter`, check `COMFY_SYNC/` has what you want first — `kill`
takes the disk with it, and the sync runs on a 60-second cycle.

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

**A ComfyUI deploy sits at "waiting for the server to become healthy".** Usually the
provisioning script downloading checkpoints — `photoreal.sh` pulls about 14 GB.
There's no progress bar for it: the byte counter `init` shows for language models
works off a known download size, and a provisioning script's size isn't known.
`mycodeagent log <id>` shows its `[provisioning]` lines, which is where to look.

**Look at the logs.** `mycodeagent log <id>` fetches the instance's bootstrap output.
Note that llama.cpp prints nothing while downloading the model, so silence there is
normal — `init` reports progress separately.

## All commands

| | |
|---|---|
| `login` | Store the vast.ai key + HF token, upload your SSH key |
| `models` | Catalog with live pricing |
| `init <model>` | Rent, start, tunnel. `--country`, `--provisioning`, `--sync-folder`, `--create-instance-only` |
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

- `~/.mycodeagent/config.yaml` — API key, HF token, CivitAI token, base port
- `~/.mycodeagent/mycodeagent.db` — instances and the bad-host list
- `./COMFY_SYNC/` — created in the working directory while a ComfyUI or Jupyter
  instance is running, unless `--sync-folder` pointed it somewhere else; see
  [Getting your work back](#getting-your-work-back)
- `VASTAI_API_KEY` / `HF_TOKEN` override the config file

For a language model, nothing on the rented machine is worth keeping: it's destroyed
on `kill` and the weights re-download next time. For the other two engines that is
not true, which is what `COMFY_SYNC` exists for.

## How it works, briefly

`init` searches vast.ai for the cheapest verified offer meeting the entry's VRAM,
disk and bandwidth requirements, creates an instance, waits for SSH, opens a local
port forward to the server, and waits for it to answer. Every step shares one
deadline; if any of them misses it, the instance is destroyed.

Which image and which port depends on the entry: `ghcr.io/ggml-org/llama.cpp` on
8000 for the language models, `vastai/comfy` on 8188, `vastai/pytorch` on 8888. The
lifecycle is identical — that's the point of the engine abstraction.

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
