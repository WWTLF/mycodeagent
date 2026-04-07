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

func (e *LMStudioEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	_ = numGPUs // lms load --gpu max already uses every visible CUDA device
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")

	// Install LM Studio CLI + aria2 for parallel GGUF downloads. `lms get` uses
	// a single HTTP connection with no hf_transfer support, so for a ~14 GB GGUF
	// on a multi-gigabit host it leaves most of the bandwidth on the floor.
	// aria2c -x 16 opens 16 parallel range requests against HF's CDN, typically
	// 5-10x faster on well-connected vast.ai hosts.
	b.WriteString("apt-get update && apt-get install -y curl aria2 libatomic1 libgomp1\n")
	b.WriteString("curl -fsSL https://lmstudio.ai/install.sh | bash\n")
	b.WriteString("export PATH=\"$HOME/.lmstudio/bin:$PATH\"\n\n")

	// Bootstrap is required on a fresh install before the daemon will come up
	// in headless containers; without it `lms daemon up` times out.
	b.WriteString("lms bootstrap\n")
	b.WriteString("lms daemon up\n")

	// Download GGUF directly into LM Studio's cache dir so `lms load` finds it
	// without needing `lms get`. Layout: /root/.lmstudio/models/{org}/{repo}/{file}.gguf
	modelDir := fmt.Sprintf("/root/.lmstudio/models/%s", model.HFRepo)
	downloadURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", model.HFRepo, model.GGUFFile)
	fmt.Fprintf(&b, "mkdir -p '%s'\n", modelDir)
	fmt.Fprintf(&b, "if [ ! -f '%s/%s' ]; then\n", modelDir, model.GGUFFile)
	fmt.Fprintf(&b, "  aria2c -x 16 -s 16 -k 1M --continue=true --max-tries=5 --retry-wait=5 --file-allocation=none --console-log-level=warn --summary-interval=10 -d '%s' -o '%s' '%s'\n",
		modelDir, model.GGUFFile, downloadURL)
	b.WriteString("fi\n")
	// lms stores models with lowercase short names; load the first available LLM.
	// contextLength is the runtime value computed by DeployService (scaled for the offer);
	// fall back to the model's baseline if the caller didn't pass one.
	ctx := contextLength
	if ctx <= 0 {
		ctx = model.ContextLength
	}
	if ctx > 0 {
		fmt.Fprintf(&b, "lms load --gpu max --context-length %d --yes\n", ctx)
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

func (e *LMStudioEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	_ = numGPUs
	_ = contextLength
	quant := extractQuant(model.GGUFFile)
	if quant != "" {
		return fmt.Sprintf("lms bootstrap && lms daemon up && lms get 'https://huggingface.co/%s@%s' --yes && lms load --gpu max --yes && lms server start --port 8000",
			model.HFRepo, quant)
	}
	return fmt.Sprintf("lms bootstrap && lms daemon up && lms get 'https://huggingface.co/%s' --yes && lms load --gpu max --yes && lms server start --port 8000",
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
