package entity

import "time"

type ModelCategory string

const (
	CategoryCoding  ModelCategory = "coding"
	CategoryFiction ModelCategory = "fiction"
	CategoryDolphin ModelCategory = "dolphin"
)

type ModelEngine string

const (
	EngineVLLM     ModelEngine = "vllm"
	EngineLMStudio ModelEngine = "lmstudio"
)

type Model struct {
	Name           string
	Alias          string
	HFRepo         string
	Category       ModelCategory
	VRAM           int           // GB required per GPU
	NumGPUs        int           // number of GPUs needed (0 or 1 = single GPU)
	StartupTimeout time.Duration // max time to wait for instance + vLLM to become healthy
	Engine         ModelEngine   // "vllm" (default) or "lmstudio"
	VLLMArgs       []string      // vLLM-specific serve arguments
	GGUFFile       string        // LM Studio-specific: GGUF filename to download
}
