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
	b.WriteString("apt-get update && apt-get install -y curl libatomic1\n")
	b.WriteString("curl -fsSL https://lmstudio.ai/install.sh | bash\n")
	b.WriteString("export PATH=\"$HOME/.lmstudio/bin:$PATH\"\n\n")

	// Start daemon, download model, load it, start server
	b.WriteString("lms daemon up\n")
	fmt.Fprintf(&b, "lms get 'https://huggingface.co/%s' --yes\n", model.HFRepo)
	fmt.Fprintf(&b, "lms load '%s' --gpu max --yes\n", model.HFRepo)
	b.WriteString("lms server start --port 8000\n")

	script := b.String()
	escaped := strings.ReplaceAll(script, "'", "'\\''")
	return fmt.Sprintf("echo '%s' > /tmp/start_lmstudio.sh && chmod +x /tmp/start_lmstudio.sh && bash /tmp/start_lmstudio.sh 2>&1 | tee /tmp/lmstudio.log", escaped)
}

func (e *LMStudioEngine) BuildRawCommand(model *entity.Model) string {
	return fmt.Sprintf("lms daemon up && lms get 'https://huggingface.co/%s' --yes && lms load '%s' --gpu max --yes && lms server start --port 8000",
		model.HFRepo, model.HFRepo)
}

func (e *LMStudioEngine) VolumeMountPath() string {
	return "/root/.lmstudio/models"
}

func (e *LMStudioEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = "export PATH=\"$HOME/.lmstudio/bin:$PATH\"; lms server stop 2>/dev/null; lms daemon down 2>/dev/null; sleep 2"
	startCmd = "nohup bash /tmp/start_lmstudio.sh 2>&1 | tee /tmp/lmstudio.log &"
	return
}
