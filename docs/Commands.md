# Command reference

Every command, every flag, what it is for, and a worked example. The
[README](../README.md) is the narrative introduction; this is the lookup table.

## Contents

- [Install](#install)
- [Global flags](#global-flags)
- [Which ID goes where](#which-id-goes-where)
- [Setup: `login`, `models`, `info`](#setup)
- [Deploying: `init`](#deploying)
- [Running instances: `ps`, `tunnel`, `restart`, `log`, `config`](#running-instances)
- [Finishing: `kill`, `stop`, `start`](#finishing)
- [Money and hosts: `budget`, `hosts`](#money-and-hosts)
- [Recipes](#recipes)

## Install

```bash
go install github.com/WWTLF/mycodeagent/cmd/mycodeagent@latest
```

That puts the binary in `$(go env GOPATH)/bin` — make sure it is on your `PATH`:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
mycodeagent --version        # mycodeagent version v0.3.0
```

Pin a version instead of tracking the latest release:

```bash
go install github.com/WWTLF/mycodeagent/cmd/mycodeagent@v0.3.0
```

The version is reported even from a `go install` build, because it falls back to
the module version Go records in `debug.BuildInfo` when no `-ldflags` stamp is
present. A build from a git checkout gets the stamp instead and reports the exact
commit (`v0.3.0-3-gabc1234`).

Prebuilt tarballs are on the [releases
page](https://github.com/WWTLF/mycodeagent/releases) for linux and darwin,
amd64 and arm64.

**Requirements**

| | |
|---|---|
| Go | 1.26.1+, only to build. Not needed for the tarballs. |
| `ssh` | On `PATH`. Every tunnel and remote command shells out to it. |
| SSH keypair | In `~/.ssh`. `login` uploads the public half to vast.ai. |
| `rsync` | Only for ComfyUI and Jupyter — it is what copies your work back. Without it the deploy still succeeds and warns. |
| OS | Linux, macOS. Windows is not built: the tunnel uses `Setpgid` and `syscall.Kill`. |

## Global flags

Available on every command.

| Flag | Purpose |
|---|---|
| `-v`, `--verbose` | Log every vast.ai API request and response. The first thing to reach for when an offer search returns nothing or a deploy behaves oddly. |
| `--version` | Print the version and exit. |
| `-h`, `--help` | Help for any command. `init --help` is worth reading — it carries the country list and the provisioning explanation. |

## Which ID goes where

Two different numbers identify an instance, and they are not interchangeable.

| ID | What it is | Shown as |
|---|---|---|
| **Local ID** | A small number this tool assigns (`1`, `2`, `47`). | `ID` column in `ps` |
| **vast.ai ID** | The 8-digit id vast.ai assigns (`46685188`). | `VAST ID` column in `ps` |

Almost every command takes the **local** ID:

```bash
mycodeagent kill 47          # local
mycodeagent log 47           # local
mycodeagent stop 47          # local
```

**`tunnel` is the exception — it takes the vast.ai ID:**

```bash
mycodeagent tunnel 46685188  # vast.ai
```

It looks the instance up by `vastai_id`, because re-attaching a tunnel is what you
do when local state and reality have drifted apart. Run `ps` and read the right
column.

## Setup

### `login`

```
mycodeagent login
```

Interactive. Stores credentials in `~/.mycodeagent/config.yaml` and uploads your
SSH public key to vast.ai so rented instances accept it. Run it once.

It asks for three things. Press Enter to keep the current value — existing values
are shown masked:

| Prompt | Required | Purpose |
|---|---|---|
| Vast.ai API key | **Yes** | Everything. Get one at <https://cloud.vast.ai/manage-keys/>. Verified before it is saved. |
| HuggingFace token | No | Only for gated repos. Nothing in the current catalogue needs it. Get a read token at <https://huggingface.co/settings/tokens>. |
| CivitAI token | No | Only for a provisioning script fetching gated image checkpoints. |

It then offers to upload `~/.ssh/id_*.pub`. Say yes unless the key is already
registered — a key vast.ai does not have means every deploy fails at the SSH
phase, and the machine gets blacklisted for it.

### `models`

```
mycodeagent models
```

Lists the catalogue with **live** pricing — the cheapest matching offer at that
moment, so the numbers move. Columns `R`/`V`/`T` are reasoning, vision,
tool-calling.

Prices are for the *global* pool. Adding `--country` to `init` can cost
noticeably more, because a small country may have no offer at the cheap tier at
all.

### `info`

```
mycodeagent info [id]
```

Runtime summary: what is deployed, on which port, and how to point a client at
it. With a local ID, just that instance.

## Deploying

### `init`

```
mycodeagent init <model> [flags]
```

The main command. Searches for the cheapest offer meeting the model's
requirements, rents it, starts the engine, opens an SSH tunnel, and waits until
the service answers. **A failed startup destroys the instance automatically**, so
a broken `init` never leaves a paid GPU running. Ctrl-C does the same.

`<model>` is a name or an alias from `models` — `coder`, `qwen36-27b-24g`,
`jupyter`, `comfyui` all work.

| Flag | Default | Purpose |
|---|---|---|
| `--country <codes>` | anywhere | Restrict the offer search to ISO-3166 alpha-2 codes, comma-separated: `--country RO,DE`. The search sorts purely by price, which is how three deploys in a row can land in a region whose route to HuggingFace runs at ~1 MB/s. Codes are validated first — vast.ai answers an unknown code with an empty result set that is indistinguishable from "your filters are too tight". |
| `--sync-folder <path>` | `./workspace` | Where the instance's files are kept locally. Used as given: a one-directory engine (Jupyter) syncs into it directly, ComfyUI keeps `output/` and `workflows/` under it. Resolved to an absolute path and stored on the instance, so a later `tunnel` or `start` from a different directory syncs to the same place. Ignored by engines that write nothing. |
| `--provisioning <url>` | none | URL of a shell script the instance downloads and runs before its service starts. The supported way to get checkpoints and LoRAs onto a disposable machine. ComfyUI only; llama.cpp ignores it. The script runs on your rented machine **with your HF and CivitAI tokens in its environment**, so point it only at something you control. |
| `--create-instance-only` | off | Create the instance and print SSH details, then stop. No tunnel, no health check, no auto-destroy. For debugging a deploy by hand. |

```bash
mycodeagent init coder                              # cheapest 24 GB card, anywhere
mycodeagent init coder-max --country NO,SE,DK       # Nordics only
mycodeagent init jupyter --sync-folder ~/notebooks  # notebooks in ~/notebooks/workspace/
mycodeagent init comfyui \
  --provisioning https://raw.githubusercontent.com/WWTLF/mycodeagent/main/config/provisioning/photoreal.sh
```

Startup takes 15–30 minutes of budget depending on the model, most of it the host
pulling the image and the GGUF downloading. Progress is reported as it goes.

## Running instances

### `ps`

```
mycodeagent ps
```

Syncs with vast.ai and lists what you have: local ID, vast ID, status, alias,
health, and the tunnel URL. The URL is the one to open or point a client at.

### `tunnel`

```
mycodeagent tunnel <vastai_id>
```

Re-attaches a dead SSH tunnel — **takes the vast.ai ID**, see [above](#which-id-goes-where).
Reads a fresh SSH host and port from the API (they are reassigned on resume) and
restarts the file sync alongside it, into the folder the deploy chose.

### `restart`

```
mycodeagent restart <id>
```

Regenerates the startup script on the instance and restarts the server, without
renting anything new. For a model server that died on a machine still worth
keeping. Monitor with `mycodeagent log <id>`.

### `log`

```
mycodeagent log <id> [-n <lines>]
```

Fetches the vast.ai bootstrap log — the host's own record of pulling the image
and running the onstart script. The first place to look when a deploy fails.

| Flag | Default | Purpose |
|---|---|---|
| `-n`, `--tail` | `100` | Lines from the end. |

### `config`

```
mycodeagent config
```

Writes every running instance into `~/.config/opencode/opencode.jsonc` as a
provider, so opencode can talk to them without hand-editing JSON. Only language
models are written — `comfyui` and `jupyter` have no OpenAI-compatible API.

## Finishing

### `kill`

```
mycodeagent kill <id>
```

Destroys the instance. **This is the normal way to finish** — it is the only one
that stops all billing.

It takes the container disk with it. For ComfyUI and Jupyter, check your sync
folder has what you want first: the loop runs on a 60-second cycle.

### `stop` / `start`

```
mycodeagent stop <id>
mycodeagent start <id>
```

`stop` releases the GPU but keeps the instance and its container disk, which
**keeps billing** at roughly $0.15/GB/month. `start` resumes it and opens a fresh
tunnel; the model is still in the container-disk cache, so nothing is
re-downloaded.

**`stop` never wins on price.** A day of idle 28 GB disk is ~$0.14 against ~$0.004
of re-download after a `kill`. It exists for the non-price cases: holding a host
that turned out to be good, or pausing mid-debugging.

A failed `start` deliberately does *not* destroy the instance — you paid storage
to keep it, and a transient SSH error is no reason to discard it. It leaves a
billing GPU and says so.

## Money and hosts

### `budget`

```
mycodeagent budget
```

Spend so far and the current run rate, per instance and in total.

### `hosts`

```
mycodeagent hosts list
mycodeagent hosts remove <machine_id>
mycodeagent hosts clear
```

Manages the bad-host blacklist. A machine is recorded when it fails in a way that
is provably the *host's* fault — the instance never reached running, SSH never
came up, or a startup timeout followed a provisioning phase that alone ran past
5 minutes. Blacklisted machines are skipped by future searches.

A model server **crash** never blames the host: that means a bad quant or
insufficient VRAM, and charging it to the machine is how one bad catalogue entry
would blacklist every good host until the search returns "no offers found".

**If every deploy fails at the SSH phase, suspect your key, not the hosts.** An
unregistered or passphrase-protected key is refused by every machine identically,
and each refusal blacklists a healthy one. Run `login`, then `hosts clear`.

## Recipes

**A coding session, start to finish**

```bash
mycodeagent init coder          # ~$0.12/hr, 15 min
mycodeagent config              # point opencode at it
# ... work ...
mycodeagent kill 1              # the only thing that stops billing
```

**A notebook whose work you keep**

```bash
mycodeagent init jupyter-mini --sync-folder ~/notebooks
# open the printed http://localhost:PORT
# ... work; files land in ~/notebooks/workspace/ every 60s ...
mycodeagent kill 1
```

Next time, `init` uploads what is already in `~/notebooks/workspace/` before
pulling anything down, so the new instance starts with your notebooks.

**A deploy that hangs**

```bash
mycodeagent ps                  # is it running? healthy?
mycodeagent log 1 -n 300        # what did the host actually do?
mycodeagent -v ps               # every API call, if the above is not enough
```

**Nothing matches the search**

```bash
mycodeagent hosts list          # blacklist drained the pool?
mycodeagent hosts clear
mycodeagent -v init coder       # see the actual filters being sent
```
