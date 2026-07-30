package entity

import "time"

type ModelCategory string

const (
	CategoryCoding ModelCategory = "coding"
)

type Model struct {
	Name             string
	Alias            string
	HFRepo           string // HuggingFace GGUF repository, e.g. "unsloth/Qwen3-8B-GGUF"
	Quant            string // quant tag inside HFRepo, e.g. "UD-Q4_K_XL" (empty = llama.cpp picks Q4_K_M)
	Category         ModelCategory
	VRAM             int           // GB required per GPU (baseline)
	NumGPUs          int           // GPU count to rent; SearchOffers matches it with `eq`, so the offer always has exactly this many
	DiskGB           int           // container disk to request on vast.ai; sized to hold the image + GGUF download + scratch (0 = default)
	StartupTimeout   time.Duration // max time to wait for instance + model server to become healthy
	ContextLength    int           // baseline context length sized for VRAM (0 = engine default)
	MaxContextLength int           // architectural ceiling; DeployService scales ContextLength up toward this when the offer has more VRAM per GPU than VRAM. 0 = no scaling.
	LlamaArgs        []string      // llama-server arguments; must NOT contain engine-owned flags (-hf, -m, --host, --port, -a, -c, -ngl) — those are injected from runtime values
	Reasoning        bool          // model supports thinking/reasoning
	Vision           bool          // model supports image inputs
	ToolCalling      bool          // model supports tool/function calling
}
