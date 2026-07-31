#!/bin/bash
#
# Photorealistic setup for `mycodeagent init comfyui`.
#
#   mycodeagent init comfyui \
#     --provisioning https://raw.githubusercontent.com/WWTLF/mycodeagent/main/config/provisioning/photoreal.sh
#
# Two jobs, and the second is the one that makes this more than a download list:
#
#   1. Download a small core set, so the UI opens on something that can already
#      generate photorealistic portraits.
#
#   2. Register *every* model below with ComfyUI-Manager, so the rest appear in
#      its Model Manager and install with one click, on demand. Registration is
#      cheap — it is a JSON entry, not a download — which is why the catalogue
#      can be long while the boot stays short.
#
# Why registration is required at all, rather than pasting a link into the UI:
# ComfyUI-Manager's install endpoint calls check_whitelist_for_model() before it
# will fetch anything, and that matches the request against model-list.json on
# (save_path, base, filename). The check is unconditional — no security level
# relaxes it — so an arbitrary URL is answered with "Invalid model install
# request" no matter what. Adding entries to that list is the supported way in.
#
# Self-contained on purpose. The image's own helper functions come from a boot
# script that vast.ai's runtype "ssh" prevents from running, so everything here
# is plain curl.
#
# Tokens, if configured via `mycodeagent login`, arrive as environment
# variables. Every URL below was verified to download anonymously, so none are
# needed — but HF_TOKEN and CIVITAI_TOKEN are used when present.

set -u

WORKSPACE="${WORKSPACE:-/workspace}"
# The engine resolves the real tree and exports both of these before running
# this script; the defaults only matter if it is run by hand.
COMFYUI_DIR="${COMFYUI_DIR:-/opt/workspace-internal/ComfyUI}"
COMFYUI_PYTHON="${COMFYUI_PYTHON:-/venv/main/bin/python}"

NODE_DIR="${COMFYUI_DIR}/custom_nodes"

log() { printf '[provisioning] %s\n' "$*"; }

# ---------------------------------------------------------------------------
# Catalogue
# ---------------------------------------------------------------------------
#
# Fields: preload|type|base|filename|size|url|name|description
#
#   preload  yes = download now; no = register only, install from the Manager
#   type     drives the destination folder via the Manager's own map:
#            checkpoint→checkpoints  vae→vae  upscale→upscale_models
#            clip→text_encoders  lora→loras  unet/diffusion_model→diffusion_models
#   base     free text, but the Manager filters on it — keep to the values it
#            already uses: SDXL, SD1.5, FLUX.1, upscale, t5
#
# Every URL here was checked live: HTTP 200/206 with an octet-stream body.
# Note that civitai answers HEAD with 403 and GET with 206, so verify these with
# `curl -r 0-255`, never `curl -I` — a HEAD check reports every one as broken.
MODELS=(
# --- core set, downloaded at boot (~14 GB) --------------------------------
"yes|checkpoint|SDXL|RealVisXL_V5.0_fp16.safetensors|6.94GB|https://huggingface.co/SG161222/RealVisXL_V5.0/resolve/main/RealVisXL_V5.0_fp16.safetensors|RealVis XL V5.0|SDXL fine-tune aimed at photographic realism of people — skin texture, eyes and hands are where stock SDXL falls apart. ~28 steps, CFG 4.5."
"yes|checkpoint|SDXL|RealVisXL_V5.0_Lightning_fp16.safetensors|6.94GB|https://huggingface.co/SG161222/RealVisXL_V5.0_Lightning/resolve/main/RealVisXL_V5.0_Lightning_fp16.safetensors|RealVis XL V5.0 Lightning|Same model distilled for 4-6 steps at CFG 1.5. Draft on this, finish on the full one — the usual settings will blow it out."
"yes|vae|SDXL|sdxl_vae_fp16_fix.safetensors|0.33GB|https://huggingface.co/madebyollin/sdxl-vae-fp16-fix/resolve/main/sdxl_vae.safetensors|SDXL VAE fp16-fix|SDXL's own VAE emits black images in fp16 on some cards. This is the standard fix and what most SDXL workflows expect."
"yes|upscale|upscale|4x-UltraSharp.pth|0.07GB|https://huggingface.co/Kim2091/UltraSharp/resolve/main/4x-UltraSharp.pth|4x-UltraSharp|Takes a 1024px render to 4096 without the plastic look a plain resize gives."
"yes|lora|SDXL|add-detail-xl.safetensors|0.23GB|https://civitai.com/api/download/models/135867|Detail Tweaker XL|Detail slider with no trigger word — positive strength sharpens skin texture and fabric, negative smooths. The person.json workflow loads it at 0.6. Cheap enough to preload."

# --- SDXL, registered only -------------------------------------------------
"no|lora|SDXL|DetailedEyes_V3.safetensors|0.09GB|https://civitai.com/api/download/models/145907|DetailedEyes XL|Fixes the dead, low-detail eyes SDXL tends to give at a distance. Stack after Detail Tweaker at ~0.5."
"no|checkpoint|SDXL|sd_xl_base_1.0.safetensors|6.94GB|https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/resolve/main/sd_xl_base_1.0.safetensors|SDXL base 1.0|The unmodified base model. Worth having as a reference point when a fine-tune behaves oddly."
"no|checkpoint|SDXL|ponyDiffusionV6XL.safetensors|6.94GB|https://civitai.com/api/download/models/290640|Pony Diffusion V6 XL|Very strong prompt adherence for characters and poses; stylised rather than photoreal. Needs its own score_9-style prompt prefix."
"no|checkpoint|SDXL|juggernautXL_ragnarok.safetensors|7.11GB|https://civitai.com/api/download/models/1759168|Juggernaut XL|General-purpose SDXL fine-tune, strong on photographic lighting and landscapes."
"no|checkpoint|SDXL|waiIllustriousSDXL_v170.safetensors|6.94GB|https://civitai.com/api/download/models/2883731|WAI-illustrious SDXL|Illustrious-based anime/illustration model. Not photoreal — included as the other end of the range."

# --- SD1.5, registered only. Small, fast, and still the best-served -------
# --- ecosystem for LoRAs and ControlNets ----------------------------------
"no|checkpoint|SD1.5|realisticVisionV60B1_v51HyperVAE.safetensors|2.13GB|https://civitai.com/api/download/models/501240|Realistic Vision V6.0 B1|The long-standing SD1.5 photorealism benchmark. A quarter the size of an SDXL checkpoint and correspondingly quicker."
"no|checkpoint|SD1.5|epicrealism_naturalSinRC1VAE.safetensors|2.13GB|https://civitai.com/api/download/models/143906|epiCRealism|SD1.5 photoreal fine-tune with a softer, more natural look than Realistic Vision."
"no|checkpoint|SD1.5|dreamshaper_8.safetensors|2.13GB|https://civitai.com/api/download/models/128713|DreamShaper 8|Versatile SD1.5 model that holds up across photo and illustration prompts."
"no|checkpoint|SD1.5|majicmixRealistic_v7.safetensors|2.13GB|https://civitai.com/api/download/models/176425|majicMIX realistic|SD1.5 fine-tune tuned for East Asian portraiture."
"no|checkpoint|SD1.5|chilloutmix_NiPrunedFp32Fix.safetensors|4.27GB|https://civitai.com/api/download/models/11745|ChilloutMix|Older SD1.5 portrait model, kept because a large number of community LoRAs were trained against it."

# --- FLUX text encoders ----------------------------------------------------
"no|clip|t5|t5xxl_fp8_e4m3fn.safetensors|4.89GB|https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/t5xxl_fp8_e4m3fn.safetensors|T5-XXL fp8 text encoder|Needed by FLUX. Public, unlike the FLUX transformers."
"no|clip|FLUX.1|clip_l.safetensors|0.25GB|https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/clip_l.safetensors|CLIP-L text encoder|The second of FLUX's two text encoders."

# --- Additional SDXL checkpoints -------------------------------------------
# Preloaded, not registered-only, and the reason is the same one that keeps FLUX
# out of the list: this is the single catalogue entry that 401s without a token.
# The Manager's downloader sends no Authorization header, so a register-only
# entry here would sit in the UI and fail every time it was clicked. fetch()
# does send CIVITAI_TOKEN, so downloading it at boot works — at the cost of
# 6.5 GB and about two minutes. Drop this row if you would rather have the
# shorter boot.
"yes|checkpoint|SDXL|cyberrealisticXL_v100.safetensors|6.46GB|https://civitai.com/api/download/models/2840768|CyberRealistic XL v10.0|Strong photoreal SDXL model with excellent skin texture. CFG 3-5, 30+ steps, DPM++ 2M SDE Karras. Needs CIVITAI_TOKEN, so it is preloaded rather than installed from the Manager."
"no|checkpoint|SDXL|talmendoxlSDXL_v11Beta.safetensors|6.46GB|https://civitai.com/api/download/models/131960|TalmendoXL v1.1-Beta|SDXL fine-tune with natural lighting and good body variety. Uncensored."

# --- Video models ----------------------------------------------------------
#
# Three things differ from the image models above, and all three were wrong in
# the first cut of this section:
#
#   1. A repository page is not a download. https://huggingface.co/Wan-AI/... is
#      HTML; the file lives under /resolve/main/<path>. Four of the six original
#      entries returned a 200 full of HTML, which is exactly the case fetch()'s
#      HTML guard exists for — the Manager would have written a web page out
#      under a .safetensors name.
#
#   2. type is diffusion_model, not checkpoint. These are bare transformers, so
#      they belong in models/diffusion_models and load through UNETLoader, not
#      CheckpointLoaderSimple.
#
#   3. A transformer alone cannot generate. Each family needs its VAE and text
#      encoder as separate files, listed here alongside it. Install the set.
#
# Sizes are real, and they are the binding constraint: the comfyui catalogue
# entry asks for a 60 GB container disk, of which the image and the core set
# above already use roughly 35 GB. Wan 5B fits. Wan 14B fp8 (two stages, 28.6 GB
# together) does not, alongside everything else, and LTX-2.3 at 46 GB per file
# fits nothing else at all. Raise Model.DiskGB in the catalogue before reaching
# for those, or start from a ComfyUI instance with the core set trimmed.

# Wan 2.2 TI2V 5B — the one that fits. Needs the VAE and umt5 below.
"no|diffusion_model|Wan2.2|wan2.2_ti2v_5B_fp16.safetensors|10.0GB|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_ti2v_5B_fp16.safetensors|Wan 2.2 TI2V 5B|Text- and image-to-video in one 5B model. The practical choice on a 60 GB disk; 14B needs a bigger one."
"no|vae|Wan2.2|wan2.2_vae.safetensors|1.4GB|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/vae/wan2.2_vae.safetensors|Wan 2.2 VAE|Required by every Wan 2.2 model."
"no|clip|Wan2.2|umt5_xxl_fp8_e4m3fn_scaled.safetensors|6.7GB|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors|UMT5-XXL fp8|Wan's text encoder. fp8 rather than the 11.4 GB fp16 to keep the set on disk."

# Wan 2.2 I2V 14B, fp8. Two stages, and both are required — high noise runs the
# early steps, low noise the late ones. 28.6 GB together, so disk first.
"no|diffusion_model|Wan2.2|wan2.2_i2v_high_noise_14B_fp8_scaled.safetensors|14.3GB|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_i2v_high_noise_14B_fp8_scaled.safetensors|Wan 2.2 I2V 14B high-noise|Stage one of the 14B image-to-video pair. Useless without the low-noise half."
"no|diffusion_model|Wan2.2|wan2.2_i2v_low_noise_14B_fp8_scaled.safetensors|14.3GB|https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/resolve/main/split_files/diffusion_models/wan2.2_i2v_low_noise_14B_fp8_scaled.safetensors|Wan 2.2 I2V 14B low-noise|Stage two of the 14B image-to-video pair."

# HunyuanVideo 1.5 — 720p image-to-video, fp8. ~20 GB for the full set.
"no|diffusion_model|Hunyuan1.5|hunyuanvideo1.5_720p_i2v_cfg_distilled_fp8_scaled.safetensors|8.3GB|https://huggingface.co/Comfy-Org/HunyuanVideo_1.5_repackaged/resolve/main/split_files/diffusion_models/hunyuanvideo1.5_720p_i2v_cfg_distilled_fp8_scaled.safetensors|HunyuanVideo 1.5 720p I2V|CFG-distilled, so it runs at CFG 1. Lighter than Wan 14B for comparable 720p output."
"no|vae|Hunyuan1.5|hunyuanvideo15_vae_fp16.safetensors|2.5GB|https://huggingface.co/Comfy-Org/HunyuanVideo_1.5_repackaged/resolve/main/split_files/vae/hunyuanvideo15_vae_fp16.safetensors|HunyuanVideo 1.5 VAE|Required by HunyuanVideo 1.5."
"no|clip|Hunyuan1.5|qwen_2.5_vl_7b_fp8_scaled.safetensors|9.4GB|https://huggingface.co/Comfy-Org/HunyuanVideo_1.5_repackaged/resolve/main/split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors|Qwen2.5-VL 7B fp8|HunyuanVideo 1.5's text encoder."

# LTX-2.3 22B — 46 GB per file. Listed for completeness; it does not fit the
# current 60 GB disk next to anything, including the core set.
"no|diffusion_model|LTX|ltx-2.3-22b-distilled.safetensors|46.1GB|https://huggingface.co/Lightricks/LTX-2.3/resolve/main/ltx-2.3-22b-distilled.safetensors|LTX-2.3 22B distilled|Joint audio-video, 4-10 steps. Needs a much larger container disk than the catalogue currently requests."
"no|diffusion_model|LTX|ltx-2.3-22b-dev.safetensors|46.1GB|https://huggingface.co/Lightricks/LTX-2.3/resolve/main/ltx-2.3-22b-dev.safetensors|LTX-2.3 22B dev|The undistilled version. Same disk caveat."
)
# --- FLUX.1-dev (optional, ~24 GB) ------------------------------------------
#
# Beats SDXL on realism and prompt adherence, and it *is* reachable with a
# token — verified 200 with an accepted licence. To use it: accept the licence
# at huggingface.co/black-forest-labs/FLUX.1-dev with the same account as
# HF_TOKEN, then uncomment. FLUX needs the transformer, both text encoders and
# its own VAE; it will not load with any missing. Note text encoders belong in
# text_encoders/, not clip/, on current ComfyUI.
#
# FLUX_PRELOAD=(
#   "https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/flux1-dev.safetensors|diffusion_models/flux1-dev.safetensors"
#   "https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/t5xxl_fp8_e4m3fn.safetensors|text_encoders/t5xxl_fp8_e4m3fn.safetensors"
#   "https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/clip_l.safetensors|text_encoders/clip_l.safetensors"
#   "https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/ae.safetensors|vae/flux_ae.safetensors"
# )

# ---------------------------------------------------------------------------
# Download helpers
# ---------------------------------------------------------------------------

# dir_for TYPE — the Manager's own type→folder map, mirrored so a preload lands
# where a Manager install of the same entry would put it.
dir_for() {
    case "$1" in
        checkpoint|checkpoints|unclip) echo "checkpoints" ;;
        vae)                           echo "vae" ;;
        lora)                          echo "loras" ;;
        upscale)                       echo "upscale_models" ;;
        clip|text_encoders)            echo "text_encoders" ;;
        controlnet|t2i-adapter)        echo "controlnet" ;;
        unet|diffusion_model)          echo "diffusion_models" ;;
        embedding|embeddings)          echo "embeddings" ;;
        *)                             echo "etc" ;;
    esac
}

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
        # Civitai serves the same site, API and model ids from more than one
        # domain — civitai.red returns the identical catalogue and hands out
        # download URLs on whichever domain you asked. Matching only .com meant
        # a .red URL silently went out with no Authorization header, so anything
        # needing auth failed for a reason nothing in the log would explain.
        *civitai.com*|*civitai.red*)
            [ -n "${CIVITAI_TOKEN:-}" ] && auth=(-H "Authorization: Bearer ${CIVITAI_TOKEN}")
            ;;
    esac

    log "downloading ${name}"
    # --no-progress-meter, not because the progress is unwanted but because
    # nothing here is a terminal: the engine tees this into the log that
    # `mycodeagent log` shows, and curl's meter redraws with carriage returns
    # that a file keeps every one of. One 6 GB checkpoint buried the
    # [provisioning] lines under a couple of hundred kilobytes of counter.
    if curl -fL --no-progress-meter --retry 3 --retry-delay 5 --connect-timeout 20 \
            "${auth[@]}" -o "${dest}.part" "${url}"; then
        # -f rejects a 4xx, but not a 200 that is the wrong thing. A model host
        # that wants credentials often *redirects* to a login page, which is a
        # perfectly successful 200 full of HTML, and curl will happily write it
        # out under a .safetensors name. The failure then surfaces hours later as
        # a checkpoint ComfyUI refuses to load, with nothing in the log to say
        # why. Cheaper to notice here.
        if head -c 512 "${dest}.part" | grep -qiE '<!doctype html|<html[ >]'; then
            rm -f "${dest}.part"
            log "FAILED: ${name} — server returned an HTML page, not a model"
            log "        (usually a login redirect: check HF_TOKEN / CIVITAI_TOKEN)"
            return 1
        fi
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

# ---------------------------------------------------------------------------
# ComfyUI-Manager registration
# ---------------------------------------------------------------------------
#
# Merges the catalogue into the Manager's model-list.json and points its
# database at that file.
#
# db_mode decides where the Model Manager reads its list from: local, cache or
# remote, defaulting to cache (the upstream channel over HTTP). Our entries live
# in the local file, so without switching this they would be installable but
# invisible — the install path checks the local list too, the UI does not read
# it. Merging rather than replacing keeps the ~538 entries the image ships.
#
# The cost of `local` is that the node and model lists stop tracking upstream
# between image rebuilds. Switch the Manager's DB back to "channel" in its
# settings if you want the newest catalogue; the models below stay installable
# either way, because the whitelist check reads both.
register_with_manager() {
    local mgr
    mgr="$(find "${NODE_DIR}" -maxdepth 1 -iname 'comfyui-manager' -type d 2>/dev/null | head -1)"
    if [ -z "${mgr}" ] || [ ! -f "${mgr}/model-list.json" ]; then
        log "WARNING: ComfyUI-Manager not found; models will still work, but"
        log "         they will not be listed in the Manager"
        return 1
    fi

    local tsv="/tmp/provisioning-models.tsv"
    : > "${tsv}"
    local row type base filename size url name desc preload
    for row in "${MODELS[@]}"; do
        IFS='|' read -r preload type base filename size url name desc <<< "${row}"
        printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
            "${type}" "${base}" "${filename}" "${size}" "${url}" "${name}" "${desc}" >> "${tsv}"
    done

    MANAGER_DIR="${mgr}" MODELS_TSV="${tsv}" "${COMFYUI_PYTHON}" - <<'PY'
import json, os, sys

mgr = os.environ['MANAGER_DIR']
path = os.path.join(mgr, 'model-list.json')

with open(path) as fh:
    data = json.load(fh)
models = data.setdefault('models', [])

# Dedupe on the filename alone, not on the (save_path, base, filename) triple
# the install-time whitelist matches. The triple is the right identity for "may
# this be installed"; it is the wrong one for "is this already in the list",
# because the shipped catalogue reaches the same file by a different route —
# t5xxl_fp8_e4m3fn.safetensors is filed under save_path 'text_encoders/t5' there
# and 'default' here. Keying on the triple therefore added a second, visibly
# identical row for every model the image already knew: sd_xl_base_1.0, clip_l
# and t5xxl all appeared twice in the Model Manager.
#
# The shipped entry wins, being the curated one; ours is skipped. Both point at
# the same file either way.
seen = {m.get('filename') for m in models}

added = skipped = 0
with open(os.environ['MODELS_TSV']) as fh:
    for line in fh:
        line = line.rstrip('\n')
        if not line:
            continue
        type_, base, filename, size, url, name, desc = line.split('\t')
        if filename in seen:
            skipped += 1
            continue
        models.append({
            'name': name,
            'type': type_,
            'base': base,
            'save_path': 'default',
            'description': desc,
            'reference': url,
            'filename': filename,
            'url': url,
            'size': size,
        })
        seen.add(filename)
        added += 1

tmp = path + '.tmp'
with open(tmp, 'w') as fh:
    json.dump(data, fh, indent=2)
os.replace(tmp, path)
print(f'[provisioning] registered {added} model(s) with ComfyUI-Manager, '
      f'{skipped} already listed ({len(models)} entries total)')
PY

    # Point the Manager's database at the local file so the additions are
    # visible, not merely installable. Written before ComfyUI first starts, so
    # the Manager reads it instead of writing defaults.
    local cfg="${COMFYUI_DIR}/user/__manager/config.ini"
    mkdir -p "$(dirname "${cfg}")"
    if [ -f "${cfg}" ]; then
        if grep -q '^db_mode' "${cfg}"; then
            sed -i 's/^db_mode.*/db_mode = local/' "${cfg}"
        else
            sed -i '/^\[default\]/a db_mode = local' "${cfg}"
        fi
    else
        printf '[default]\ndb_mode = local\n' > "${cfg}"
    fi
    log "ComfyUI-Manager database set to the local list (db_mode = local)"
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

log "workspace=${WORKSPACE} comfyui=${COMFYUI_DIR}"
log "python=${COMFYUI_PYTHON}"

preload_count=0
for row in "${MODELS[@]}"; do
    IFS='|' read -r preload type base filename size url name desc <<< "${row}"
    [ "${preload}" = "yes" ] || continue
    preload_count=$((preload_count + 1))
    fetch "${url}" "${COMFYUI_DIR}/models/$(dir_for "${type}")/${filename}" || true
done
log "preloaded ${preload_count} of ${#MODELS[@]} catalogue entries; the rest are one click away in the Manager"

# ComfyUI-Manager earns its place on a disposable instance: it pulls further
# models and nodes from inside the web UI, so a missing checkpoint does not mean
# editing this file and redeploying. The vastai/comfy image already bundles it,
# so this is a no-op there and a fallback on any image that does not.
clone_node "https://github.com/ltdrdata/ComfyUI-Manager"
clone_node "https://github.com/cubiq/ComfyUI_essentials"

register_with_manager || true

log "checkpoints:"; ls -la "${COMFYUI_DIR}/models/checkpoints" 2>/dev/null || log "  (none)"
log "provisioning complete"
