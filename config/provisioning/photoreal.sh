#!/bin/bash
#
# Photorealistic portrait setup for `mycodeagent init comfyui`.
#
#   mycodeagent init comfyui \
#     --provisioning https://raw.githubusercontent.com/WWTLF/mycodeagent/main/config/provisioning/photoreal.sh
#
# Self-contained on purpose. The ai-dock image ships helper functions
# (provisioning_get_models and friends) that its own init script defines — but
# vast.ai replaces the image ENTRYPOINT under runtype "ssh", so that script never
# runs and those helpers do not exist. An earlier version of this file called
# them and would have died on "command not found". Everything here is plain
# curl.
#
# The engine's onstart runs this before starting ComfyUI, so models are in place
# when the web UI opens. ~14 GB; about six minutes at 40 MB/s.
#
# Tokens, if configured via `mycodeagent login`, arrive as environment
# variables. Nothing below needs them — every URL was verified to return 200
# anonymously — but CIVITAI_TOKEN is used when present so you can add gated
# civitai.com models without changing the download logic.

set -u

WORKSPACE="${WORKSPACE:-/workspace}"
# The engine resolves the real tree and exports both of these before running
# this script; the defaults only matter if it is run by hand. /opt/workspace-
# internal/ComfyUI is where the vastai/comfy image clones ComfyUI.
COMFYUI_DIR="${COMFYUI_DIR:-/opt/workspace-internal/ComfyUI}"
COMFYUI_PYTHON="${COMFYUI_PYTHON:-/venv/main/bin/python}"

# ComfyUI reads models from its own tree. Writing directly to the live tree is
# what works, because the image's boot sequence — which would symlink a separate
# storage layout into place — never runs under runtype "ssh".
CKPT_DIR="${COMFYUI_DIR}/models/checkpoints"
VAE_DIR="${COMFYUI_DIR}/models/vae"
LORA_DIR="${COMFYUI_DIR}/models/loras"
UPSCALE_DIR="${COMFYUI_DIR}/models/upscale_models"
NODE_DIR="${COMFYUI_DIR}/custom_nodes"

log() { printf '[provisioning] %s\n' "$*"; }

# fetch URL DEST — skips what is already there so a re-run is cheap, and adds
# the right Authorization header per host. Failure is reported and tolerated:
# one missing checkpoint should not cost the whole instance.
fetch() {
    local url="$1" dest="$2" name
    name="$(basename "${dest}")"
    if [ -s "${dest}" ]; then
        log "already present: ${name}"
        return 0
    fi
    mkdir -p "$(dirname "${dest}")"

    local -a auth=()
    case "${url}" in
        *huggingface.co*)
            [ -n "${HF_TOKEN:-}" ] && auth=(-H "Authorization: Bearer ${HF_TOKEN}")
            ;;
        *civitai.com*)
            [ -n "${CIVITAI_TOKEN:-}" ] && auth=(-H "Authorization: Bearer ${CIVITAI_TOKEN}")
            ;;
    esac

    log "downloading ${name}"
    if curl -fL --retry 3 --retry-delay 5 --connect-timeout 20 \
            "${auth[@]}" -o "${dest}.part" "${url}"; then
        mv "${dest}.part" "${dest}"
        log "done: ${name} ($(du -h "${dest}" | cut -f1))"
    else
        rm -f "${dest}.part"
        log "FAILED: ${name} — continuing without it"
        return 1
    fi
}

clone_node() {
    local url="$1" name
    name="$(basename "${url}")"
    if [ -d "${NODE_DIR}/${name}" ]; then
        log "node already present: ${name}"
        return 0
    fi
    mkdir -p "${NODE_DIR}"
    log "installing node ${name}"
    if ! git clone --depth 1 "${url}" "${NODE_DIR}/${name}"; then
        log "FAILED to clone ${name} — continuing"
        return 1
    fi
    # A custom node with dependencies is silently skipped at load time if they
    # are missing — ComfyUI logs the ImportError and carries on without the
    # node, so the symptom is a workflow that cannot find a node it should
    # have. They must go into the same virtualenv ComfyUI imports from, which
    # the engine hands us in COMFYUI_PYTHON; a bare `pip` would hit the system
    # interpreter, where nothing ComfyUI runs would ever see them.
    if [ -f "${NODE_DIR}/${name}/requirements.txt" ]; then
        log "installing requirements for ${name}"
        "${COMFYUI_PYTHON}" -m pip install --no-cache-dir \
            -r "${NODE_DIR}/${name}/requirements.txt" \
            || log "WARNING: requirements for ${name} failed; the node may not load"
    fi
}

log "workspace=${WORKSPACE} comfyui=${COMFYUI_DIR}"

# --- checkpoints -------------------------------------------------------------
#
# RealVisXL V5.0 is an SDXL fine-tune aimed at photographic realism of people —
# skin texture, eyes and hands are where stock SDXL falls apart. fp16 halves the
# download at no visible cost at this resolution.
#
# The Lightning variant renders in 4-6 steps against 25-30, so draft with it and
# finish with the full model.
fetch "https://huggingface.co/SG161222/RealVisXL_V5.0/resolve/main/RealVisXL_V5.0_fp16.safetensors" \
      "${CKPT_DIR}/RealVisXL_V5.0_fp16.safetensors" || true
fetch "https://huggingface.co/SG161222/RealVisXL_V5.0_Lightning/resolve/main/RealVisXL_V5.0_Lightning_fp16.safetensors" \
      "${CKPT_DIR}/RealVisXL_V5.0_Lightning_fp16.safetensors" || true

# --- vae ---------------------------------------------------------------------
#
# SDXL's own VAE emits black images in fp16 on some cards; this is the standard
# fix and what most SDXL workflows expect.
fetch "https://huggingface.co/madebyollin/sdxl-vae-fp16-fix/resolve/main/sdxl_vae.safetensors" \
      "${VAE_DIR}/sdxl_vae_fp16_fix.safetensors" || true

# --- upscaler ----------------------------------------------------------------
#
# SDXL renders at 1024; this takes a portrait to 4096 without the plastic look a
# plain resize gives.
fetch "https://huggingface.co/Kim2091/UltraSharp/resolve/main/4x-UltraSharp.pth" \
      "${UPSCALE_DIR}/4x-UltraSharp.pth" || true

# --- FLUX.1-dev (optional) ---------------------------------------------------
#
# Beats SDXL on realism but is gated: accept the licence at
# huggingface.co/black-forest-labs/FLUX.1-dev with the same account as HF_TOKEN,
# then uncomment all four lines. FLUX needs the unet, both text encoders and its
# own VAE — it will not load with any missing.
#
# fetch "https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/flux1-dev.safetensors" \
#       "${COMFYUI_DIR}/models/unet/flux1-dev.safetensors" || true
# fetch "https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/t5xxl_fp8_e4m3fn.safetensors" \
#       "${COMFYUI_DIR}/models/clip/t5xxl_fp8_e4m3fn.safetensors" || true
# fetch "https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/clip_l.safetensors" \
#       "${COMFYUI_DIR}/models/clip/clip_l.safetensors" || true
# fetch "https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/ae.safetensors" \
#       "${COMFYUI_DIR}/models/vae/flux_ae.safetensors" || true

# --- custom nodes ------------------------------------------------------------
#
# ComfyUI-Manager earns its place on a disposable instance: it pulls further
# models and nodes from inside the web UI, so a missing checkpoint does not mean
# editing this file and redeploying. It reads CIVITAI_TOKEN, which is the route
# to civitai.com — where most community portrait fine-tunes and LoRAs live.
#
# The vastai/comfy image already bundles it, so this is a no-op there and a
# fallback on any image that does not.
clone_node "https://github.com/ltdrdata/ComfyUI-Manager"
clone_node "https://github.com/cubiq/ComfyUI_essentials"

log "checkpoints:"; ls -la "${CKPT_DIR}" 2>/dev/null || log "  (none)"
log "provisioning complete"
