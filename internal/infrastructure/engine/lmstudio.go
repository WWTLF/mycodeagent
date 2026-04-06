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
	modelDir := fmt.Sprintf("/root/.lmstudio/models/%s", model.HFRepo)

	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")
	b.WriteString("apt-get update && apt-get install -y curl python3-pip\n")
	b.WriteString("pip install huggingface-hub\n\n")

	// Download GGUF model via Python API (huggingface-cli entry point removed in v1.9+)
	b.WriteString(fmt.Sprintf("mkdir -p '%s'\n", modelDir))
	if hfToken != "" {
		fmt.Fprintf(&b, "python3 -c \"from huggingface_hub import hf_hub_download; hf_hub_download('%s', '%s', local_dir='%s', token='%s')\"\n\n",
			model.HFRepo, model.GGUFFile, modelDir, hfToken)
	} else {
		fmt.Fprintf(&b, "python3 -c \"from huggingface_hub import hf_hub_download; hf_hub_download('%s', '%s', local_dir='%s')\"\n\n",
			model.HFRepo, model.GGUFFile, modelDir)
	}

	// Install LM Studio CLI
	b.WriteString("curl -fsSL https://lmstudio.ai/install.sh | bash\n")
	b.WriteString("export PATH=\"$HOME/.lmstudio/bin:$PATH\"\n\n")

	// Start daemon, load model, start server
	b.WriteString("lms daemon up\n")
	b.WriteString(fmt.Sprintf("lms load '%s/%s' --gpu max --yes\n", model.HFRepo, model.GGUFFile))
	b.WriteString("lms server start --port 8000\n")

	script := b.String()
	escaped := strings.ReplaceAll(script, "'", "'\\''")
	return fmt.Sprintf("echo '%s' > /tmp/start_lmstudio.sh && chmod +x /tmp/start_lmstudio.sh && bash /tmp/start_lmstudio.sh 2>&1 | tee /tmp/lmstudio.log", escaped)
}

func (e *LMStudioEngine) BuildRawCommand(model *entity.Model) string {
	return fmt.Sprintf("lms daemon up && lms load '%s/%s' --gpu max --yes && lms server start --port 8000",
		model.HFRepo, model.GGUFFile)
}

func (e *LMStudioEngine) VolumeMountPath() string {
	return "/root/.lmstudio/models"
}

func (e *LMStudioEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = "export PATH=\"$HOME/.lmstudio/bin:$PATH\"; lms server stop 2>/dev/null; lms daemon down 2>/dev/null; sleep 2"
	startCmd = "nohup bash /tmp/start_lmstudio.sh 2>&1 | tee /tmp/lmstudio.log &"
	return
}
