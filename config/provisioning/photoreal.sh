#!/bin/bash
#
# Photorealistic portrait setup for `mycodeagent init comfyui`.
#
#   mycodeagent init comfyui \
#     --provisioning https://raw.githubusercontent.com/WWTLF/mycodeagent/main/config/provisioning/photoreal.sh
#
# The ai-dock image sources this before starting ComfyUI, so the models are in
# place by the time the web UI answers. That is the whole point on a disposable
# instance: weights are fetched fresh each run instead of being carried around.
#
# Every URL below was checked to return HTTP 200 without credentials, so this
# works with no tokens at all. The FLUX block is the exception and is opt-in —
# see the note further down.
#
# Roughly 14 GB of downloads. On a host doing 40 MB/s that is ~6 minutes, which
# is why the comfyui catalog entry allows 25.
#
# Total VRAM at inference: SDXL at 1024x1024 fits comfortably in 12 GB, so the
# 48 GB tier the catalog rents has room for batches and upscaling.

# --- checkpoints -------------------------------------------------------------
#
# RealVisXL V5.0 is an SDXL fine-tune aimed squarely at photographic realism of
# people — skin texture, eyes and hands are where generic SDXL falls apart, and
# this is the model most often reached for to fix that. fp16 halves the download
# with no visible quality cost at this resolution.
#
# The Lightning variant renders in 4-6 steps instead of 25-30. Keep both: draft
# with Lightning, finish with the full model.
CHECKPOINT_MODELS=(
    "https://huggingface.co/SG161222/RealVisXL_V5.0/resolve/main/RealVisXL_V5.0_fp16.safetensors"
    "https://huggingface.co/SG161222/RealVisXL_V5.0_Lightning/resolve/main/RealVisXL_V5.0_Lightning_fp16.safetensors"
)

# --- vae ---------------------------------------------------------------------
#
# SDXL's own VAE produces black images in fp16 on some cards. This is the
# standard fix and is what most SDXL workflows expect.
VAE_MODELS=(
    "https://huggingface.co/madebyollin/sdxl-vae-fp16-fix/resolve/main/sdxl_vae.safetensors"
)

# --- upscaler ----------------------------------------------------------------
#
# 4x-UltraSharp is the usual second pass for portraits: SDXL renders at 1024,
# this takes it to 4096 without the plastic look a naive resize gives.
ESRGAN_MODELS=(
    "https://huggingface.co/Kim2091/UltraSharp/resolve/main/4x-UltraSharp.pth"
)

# --- unet / clip -------------------------------------------------------------
#
# Empty by default. FLUX.1-dev beats SDXL on photorealism, but its weights are
# gated: you must accept the licence at huggingface.co/black-forest-labs/FLUX.1-dev
# with the same account as your HF_TOKEN, or these 401 and provisioning fails
# halfway. Uncomment all three together — FLUX needs the unet, both text
# encoders and its own VAE, and will not load with any of them missing.
UNET_MODELS=(
    # "https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/flux1-dev.safetensors"
)
CLIP_MODELS=(
    # "https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/t5xxl_fp8_e4m3fn.safetensors"
    # "https://huggingface.co/comfyanonymous/flux_text_encoders/resolve/main/clip_l.safetensors"
)

LORA_MODELS=()
CONTROLNET_MODELS=()

# --- custom nodes ------------------------------------------------------------
#
# ComfyUI-Manager earns its place on a disposable instance: it installs further
# models and nodes from inside the web UI, so a missing checkpoint does not mean
# editing this file and redeploying. It also reads CIVITAI_TOKEN, which is how
# you reach civitai.com — where most community portrait fine-tunes and LoRAs
# live, including ones with no content filtering.
NODES=(
    "https://github.com/ltdrdata/ComfyUI-Manager"
    "https://github.com/cubiq/ComfyUI_essentials"
)

APT_PACKAGES=()
PIP_PACKAGES=()

# The ai-dock image defines everything below; this only sequences it.
function provisioning_start() {
    provisioning_print_header
    provisioning_get_apt_packages
    provisioning_get_nodes
    provisioning_get_pip_packages
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/ckpt" \
        "${CHECKPOINT_MODELS[@]}"
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/unet" \
        "${UNET_MODELS[@]}"
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/lora" \
        "${LORA_MODELS[@]}"
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/controlnet" \
        "${CONTROLNET_MODELS[@]}"
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/vae" \
        "${VAE_MODELS[@]}"
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/clip" \
        "${CLIP_MODELS[@]}"
    provisioning_get_models \
        "${WORKSPACE}/storage/stable_diffusion/models/esrgan" \
        "${ESRGAN_MODELS[@]}"
    provisioning_print_end
}

provisioning_start
