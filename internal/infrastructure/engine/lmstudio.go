package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

type LMStudioEngine struct{}

func NewLMStudioEngine() *LMStudioEngine {
	return &LMStudioEngine{}
}

func (e *LMStudioEngine) DockerImage() string {
	return "nvidia/cuda:12.4.1-runtime-ubuntu22.04"
}

func (e *LMStudioEngine) BuildOnstart(model *entity.Model, hfToken string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")

	// Install LM Studio CLI (llmster)
	b.WriteString("apt-get update && apt-get install -y curl libatomic1 libgomp1\n")
	b.WriteString("curl -fsSL https://lmstudio.ai/install.sh | bash\n")
	b.WriteString("export PATH=\"$HOME/.lmstudio/bin:$PATH\"\n\n")

	// Start daemon, download model with specific quant, load it, start server
	b.WriteString("lms daemon up\n")
	quant := extractQuant(model.GGUFFile)
	if quant != "" {
		fmt.Fprintf(&b, "lms get 'https://huggingface.co/%s@%s' --yes\n", model.HFRepo, quant)
	} else {
		fmt.Fprintf(&b, "lms get 'https://huggingface.co/%s' --yes\n", model.HFRepo)
	}
	// lms stores models with lowercase short names; load the first available LLM
	if model.ContextLength > 0 {
		fmt.Fprintf(&b, "lms load --gpu max --context-length %d --yes\n", model.ContextLength)
	} else {
		b.WriteString("lms load --gpu max --yes\n")
	}
	b.WriteString("lms server start --port 8000\n")

	script := b.String()
	escaped := strings.ReplaceAll(script, "'", "'\\''")
	return fmt.Sprintf("echo '%s' > /tmp/start_lmstudio.sh && chmod +x /tmp/start_lmstudio.sh && bash /tmp/start_lmstudio.sh 2>&1 | tee /tmp/lmstudio.log", escaped)
}

// extractQuant extracts quantization from GGUF filename, e.g. "Model-Q4_K_M.gguf" → "Q4_K_M"
func extractQuant(ggufFile string) string {
	name := strings.TrimSuffix(ggufFile, ".gguf")
	if idx := strings.LastIndex(name, "-"); idx >= 0 {
		return name[idx+1:]
	}
	return ""
}

func (e *LMStudioEngine) BuildRawCommand(model *entity.Model) string {
	quant := extractQuant(model.GGUFFile)
	if quant != "" {
		return fmt.Sprintf("lms daemon up && lms get 'https://huggingface.co/%s@%s' --yes && lms load --gpu max --yes && lms server start --port 8000",
			model.HFRepo, quant)
	}
	return fmt.Sprintf("lms daemon up && lms get 'https://huggingface.co/%s' --yes && lms load --gpu max --yes && lms server start --port 8000",
		model.HFRepo)
}

func (e *LMStudioEngine) VolumeMountPath() string {
	return "/root/.lmstudio/models"
}

func (e *LMStudioEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = "export PATH=\"$HOME/.lmstudio/bin:$PATH\"; lms server stop 2>/dev/null; lms daemon down 2>/dev/null; sleep 2"
	startCmd = "nohup bash /tmp/start_lmstudio.sh 2>&1 | tee /tmp/lmstudio.log &"
	return
}
