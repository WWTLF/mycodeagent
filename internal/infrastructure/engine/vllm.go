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
	return "vllm/vllm-openai:v0.19.0"
}

func (e *VLLMEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	vllmCmd := e.buildServeCommand(model, numGPUs, contextLength)
	// Escape single quotes so the script survives being wrapped in `echo '...'`.
	// Needed for args that contain single-quoted values (e.g. --hf-overrides with a JSON blob).
	escaped := strings.ReplaceAll(vllmCmd, "'", `'\''`)
	return fmt.Sprintf("echo '%s' > /tmp/start_vllm.sh && chmod +x /tmp/start_vllm.sh && bash /tmp/start_vllm.sh 2>&1 | tee /tmp/vllm.log", escaped)
}

func (e *VLLMEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	return e.buildServeCommand(model, numGPUs, contextLength)
}

func (e *VLLMEngine) VolumeMountPath() string {
	return "/root/.cache/huggingface"
}

func (e *VLLMEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	// vLLM v1 spawns subprocesses (EngineCore, Worker_TP*) whose argv does NOT contain
	// "vllm serve", so a single pkill on that pattern leaves orphans holding GPU memory
	// and the next start fails with CUDA OOM. Catch the subprocess patterns too, then
	// SIGKILL anything still alive.
	killCmd = strings.Join([]string{
		"pkill -f 'vllm serve' 2>/dev/null",
		"pkill -f 'multiproc_executor' 2>/dev/null",
		"pkill -f 'EngineCore' 2>/dev/null",
		"pkill -f 'vllm.v1' 2>/dev/null",
		"sleep 2",
		"pkill -9 -f 'vllm' 2>/dev/null",
		"pkill -9 -f 'multiproc_executor' 2>/dev/null",
		"sleep 1",
	}, "; ")
	startCmd = "nohup bash /tmp/start_vllm.sh 2>&1 | tee /tmp/vllm.log &"
	return
}

func (e *VLLMEngine) buildServeCommand(model *entity.Model, numGPUs, contextLength int) string {
	if numGPUs <= 0 {
		numGPUs = 1
	}
	// Strip any --tensor-parallel-size / --max-model-len already in VLLMArgs so the
	// runtime values (GPU count from the offer, scaled context) are the single source
	// of truth — no drift between the search filter and what vLLM actually receives.
	args := stripFlagPair(model.VLLMArgs, "--tensor-parallel-size", "--max-model-len")

	var b strings.Builder
	fmt.Fprintf(&b, "vllm serve '%s' --host 0.0.0.0 --port 8000 --tensor-parallel-size %d", model.HFRepo, numGPUs)
	if contextLength > 0 {
		fmt.Fprintf(&b, " --max-model-len %d", contextLength)
	}
	for _, arg := range args {
		b.WriteString(" ")
		b.WriteString(arg)
	}
	return b.String()
}

// stripFlagPair removes "--flag value" pairs matching any of the given flag names.
func stripFlagPair(args []string, flags ...string) []string {
	drop := make(map[string]bool, len(flags))
	for _, f := range flags {
		drop[f] = true
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if drop[args[i]] && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		out = append(out, args[i])
	}
	return out
}
