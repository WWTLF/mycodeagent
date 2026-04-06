package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

type VLLMEngine struct{}

func NewVLLMEngine() *VLLMEngine {
	return &VLLMEngine{}
}

func (e *VLLMEngine) DockerImage() string {
	return "vllm/vllm-openai:latest"
}

func (e *VLLMEngine) BuildOnstart(model *entity.Model, hfToken string) string {
	vllmCmd := e.buildServeCommand(model)
	return fmt.Sprintf("echo '%s' > /tmp/start_vllm.sh && chmod +x /tmp/start_vllm.sh && bash /tmp/start_vllm.sh 2>&1 | tee /tmp/vllm.log", vllmCmd)
}

func (e *VLLMEngine) BuildRawCommand(model *entity.Model) string {
	return e.buildServeCommand(model)
}

func (e *VLLMEngine) VolumeMountPath() string {
	return "/root/.cache/huggingface"
}

func (e *VLLMEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = "pkill -f 'vllm serve' 2>/dev/null; sleep 2; pkill -9 -f 'vllm serve' 2>/dev/null; sleep 1"
	startCmd = "nohup bash /tmp/start_vllm.sh 2>&1 | tee /tmp/vllm.log &"
	return
}

func (e *VLLMEngine) buildServeCommand(model *entity.Model) string {
	var b strings.Builder
	fmt.Fprintf(&b, "vllm serve '%s' --host 0.0.0.0 --port 8000", model.HFRepo)
	for _, arg := range model.VLLMArgs {
		b.WriteString(" ")
		b.WriteString(arg)
	}
	return b.String()
}
