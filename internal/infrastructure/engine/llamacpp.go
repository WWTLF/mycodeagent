package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

type LlamaCppEngine struct{}

func NewLlamaCppEngine() *LlamaCppEngine {
	return &LlamaCppEngine{}
}

func (e *LlamaCppEngine) DockerImage() string {
	// Official llama.cpp CUDA server image; ships `llama-server` pre-built.
	// Debian-based, so apt-get works at startup for aria2.
	return "ghcr.io/ggml-org/llama.cpp:server-cuda"
}

func (e *LlamaCppEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	_ = hfToken // DeployService injects HF_TOKEN as an env var on the instance; the curl below reads it at runtime.
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")

	// Download via curl (HTTPS to HF CDN — aria2 would need apt-get install, but
	// vast.ai hosts routinely block outbound port 80 to ubuntu archive mirrors,
	// so apt can't be relied on. curl ships in the base image and HF's CDN
	// typically delivers 200-500 MB/s single-stream on well-connected hosts.
	// Auth header is attached only when HF_TOKEN is set (gated repos, higher rate limits).
	modelDir := e.VolumeMountPath()
	downloadURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", model.HFRepo, model.GGUFFile)
	fmt.Fprintf(&b, "mkdir -p '%s'\n", modelDir)
	fmt.Fprintf(&b, "if [ ! -f '%s/%s' ]; then\n", modelDir, model.GGUFFile)
	b.WriteString("  AUTH_HEADER=()\n")
	b.WriteString("  if [ -n \"$HF_TOKEN\" ]; then AUTH_HEADER=(-H \"Authorization: Bearer $HF_TOKEN\"); fi\n")
	fmt.Fprintf(&b, "  curl -fL --retry 5 --retry-delay 5 --retry-all-errors -C - \"${AUTH_HEADER[@]}\" -o '%s/%s' '%s'\n",
		modelDir, model.GGUFFile, downloadURL)
	b.WriteString("fi\n\n")

	b.WriteString(e.buildServeCommand(model, numGPUs, contextLength))

	script := b.String()
	escaped := strings.ReplaceAll(script, "'", `'\''`)
	return fmt.Sprintf("echo '%s' > /tmp/start_llamacpp.sh && chmod +x /tmp/start_llamacpp.sh && bash /tmp/start_llamacpp.sh 2>&1 | tee /tmp/llamacpp.log", escaped)
}

func (e *LlamaCppEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	return e.buildServeCommand(model, numGPUs, contextLength)
}

func (e *LlamaCppEngine) VolumeMountPath() string {
	return "/root/.cache/llama.cpp/models"
}

func (e *LlamaCppEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = "pkill -f 'llama-server' 2>/dev/null; sleep 2; pkill -9 -f 'llama-server' 2>/dev/null; sleep 1"
	startCmd = "nohup bash /tmp/start_llamacpp.sh 2>&1 | tee /tmp/llamacpp.log &"
	return
}

func (e *LlamaCppEngine) buildServeCommand(model *entity.Model, numGPUs, contextLength int) string {
	if numGPUs <= 0 {
		numGPUs = 1
	}
	// Strip anything the engine owns so runtime values (actual GPU count, scaled
	// context) are the single source of truth — same pattern as VLLMEngine.
	args := stripFlagPair(model.LlamaCppArgs,
		"-c", "--ctx-size",
		"-ngl", "--n-gpu-layers",
		"--host", "--port",
		"-m", "--model",
	)

	modelPath := fmt.Sprintf("%s/%s", e.VolumeMountPath(), model.GGUFFile)

	var b strings.Builder
	fmt.Fprintf(&b, "llama-server -m '%s' --host 0.0.0.0 --port 8000 -ngl 999", modelPath)
	if contextLength > 0 {
		fmt.Fprintf(&b, " -c %d", contextLength)
	}
	// Flash attention + Q8 KV cache by default. These close the VRAM math for
	// long contexts on consumer GPUs (halves KV memory, negligible quality hit).
	// Override by putting -fa/-ctk/-ctv in LlamaCppArgs.
	if !hasAnyFlag(args, "-fa", "--flash-attn") {
		b.WriteString(" -fa")
	}
	if !hasAnyFlag(args, "-ctk", "--cache-type-k") {
		b.WriteString(" -ctk q8_0")
	}
	if !hasAnyFlag(args, "-ctv", "--cache-type-v") {
		b.WriteString(" -ctv q8_0")
	}
	for _, arg := range args {
		b.WriteString(" ")
		b.WriteString(arg)
	}
	return b.String()
}

func hasAnyFlag(args []string, flags ...string) bool {
	set := make(map[string]bool, len(flags))
	for _, f := range flags {
		set[f] = true
	}
	for _, a := range args {
		if set[a] {
			return true
		}
	}
	return false
}
