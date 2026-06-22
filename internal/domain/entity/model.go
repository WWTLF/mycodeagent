package entity

import "time"

type ModelCategory string

const (
	CategoryCoding ModelCategory = "coding"
)

type Model struct {
	Name             string
	Alias            string
	HFRepo           string
	Category         ModelCategory
	VRAM             int           // GB required per GPU (baseline)
	NumGPUs          int           // preferred GPU count (the real count comes from the offer)
	DiskGB           int           // container disk to request on vast.ai; sized to hold the model download + scratch (0 = default)
	StartupTimeout   time.Duration // max time to wait for instance + model server to become healthy
	ContextLength    int           // baseline context length sized for VRAM (0 = engine default)
	MaxContextLength int           // architectural ceiling; DeployService scales ContextLength up toward this when the offer has more VRAM per GPU than VRAM. 0 = no scaling.
	VLLMArgs         []string      // vLLM serve arguments (must NOT contain --tensor-parallel-size or --max-model-len — those are injected from runtime values)
	Reasoning        bool          // model supports thinking/reasoning
	Vision           bool          // model supports image inputs
	ToolCalling      bool          // model supports tool/function calling
}
