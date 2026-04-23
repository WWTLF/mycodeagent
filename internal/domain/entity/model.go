package entity

import "time"

type ModelCategory string

const (
	CategoryCoding ModelCategory = "coding"
)

type ModelEngine string

const (
	EngineVLLM     ModelEngine = "vllm"
	EngineLMStudio ModelEngine = "lmstudio"
	EngineLlamaCpp ModelEngine = "llamacpp"
)

type Model struct {
	Name           string
	Alias          string
	HFRepo         string
	Category       ModelCategory
	VRAM             int           // GB required per GPU (baseline)
	NumGPUs          int           // preferred GPU count (the real count comes from the offer)
	StartupTimeout   time.Duration // max time to wait for instance + model server to become healthy
	Engine           ModelEngine   // "vllm" (default)
	ContextLength    int           // baseline context length sized for VRAM (0 = engine default)
	MaxContextLength int           // architectural ceiling; DeployService scales ContextLength up toward this when the offer has more VRAM per GPU than VRAM. 0 = no scaling.
	VLLMArgs         []string      // vLLM-specific serve arguments (must NOT contain --tensor-parallel-size or --max-model-len — those are injected from runtime values)
	LlamaCppArgs     []string      // llama.cpp-specific llama-server arguments (must NOT contain -c/--ctx-size, -ngl, --host, --port, -m/--model, --split-mode, -ts — those are injected from runtime values)
	GGUFFile         string        // GGUF filename to download (used by LM Studio and llama.cpp engines)
	Reasoning        bool          // model supports thinking/reasoning
	Vision           bool          // model supports image inputs
	ToolCalling      bool          // model supports tool/function calling
}
