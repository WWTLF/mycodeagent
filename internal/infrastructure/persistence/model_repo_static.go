package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

var defaultModels = []*entity.Model{
	{
		Name:             "qwen3-8b-awq",
		Alias:            "coder-mini",
		HFRepo:           "Qwen/Qwen3-8B-AWQ",
		Category:         entity.CategoryCoding,
		VRAM:             16,
		NumGPUs:          1,
		StartupTimeout:   10 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 131072,
		Engine:           entity.EngineVLLM,
		VLLMArgs: []string{
			// Auto-detect upgrades AWQ → awq_marlin on Ampere+ — don't force --quantization.
			"--dtype", "half",
			"--gpu-memory-utilization", "0.90",
			// fp8 KV halves cache memory: 5 GB weights + ~4.7 GB KV @131k fits in 16 GB.
			"--kv-cache-dtype", "fp8",
			"--max-num-seqs", "8",
			// Qwen3-8B is native 32k; YaRN factor=4 extends to 131k per the model card.
			// Single-quoted so bash doesn't brace-expand the JSON when the onstart
			// script is written via `echo '...'`.
			"--rope-scaling", `'{"rope_type":"yarn","factor":4.0,"original_max_position_embeddings":32768}'`,
			"--enable-prefix-caching",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "hermes",
			"--reasoning-parser", "qwen3",
			"--trust-remote-code",
		},
		Reasoning:   true, // hybrid: thinking on by default, /no_think disables
		Vision:      false,
		ToolCalling: true,
	},
	{
		Name:             "qwen3-30b-a3b-thinking-gguf",
		Alias:            "coder",
		HFRepo:           "unsloth/Qwen3-30B-A3B-Thinking-2507-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          1,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 262144,
		Engine:           entity.EngineLlamaCpp,
		GGUFFile:         "Qwen3-30B-A3B-Thinking-2507-UD-Q4_K_XL.gguf",
		LlamaCppArgs: []string{
			// 18.6 GB weights + 131k Q4 KV (~3 GB) fits 24 GB. Native 256k — no YaRN
			// needed; scaledContextLength grows baseline toward MaxContextLength when
			// a larger offer is rented.
			"-ctk", "q4_0",
			"-ctv", "q4_0",
		},
		Reasoning:   true, // dedicated thinking model (only mode is thinking)
		Vision:      false,
		ToolCalling: true,
	},
	{
		Name:             "qwen3-coder-30b-a3b-hq-gguf",
		Alias:            "coder-hq",
		HFRepo:           "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    262144,
		MaxContextLength: 262144,
		Engine:           entity.EngineLlamaCpp,
		GGUFFile:         "Qwen3-Coder-30B-A3B-Instruct-UD-Q6_K_XL.gguf",
		// Engine defaults (-fa -ctk q8_0 -ctv q8_0) are the right call here:
		// 25 GB weights + 256k Q8 KV (~13 GB) = ~38 GB, fits cleanly in 48 GB total.
		Reasoning:   false,
		Vision:      false,
		ToolCalling: true,
	},
	{
		Name:             "gemma4-31b-gguf",
		Alias:            "writer",
		HFRepo:           "unsloth/gemma-4-31B-it-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          1,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    262144,
		MaxContextLength: 262144,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "gemma-4-31B-it-Q4_K_M.gguf",
		Reasoning:        true,
		Vision:           false,
		ToolCalling:      true,
	},
	{
		Name:             "lumimaid-magnum-v4-12b-gguf",
		Alias:            "rude",
		HFRepo:           "bartowski/Lumimaid-Magnum-v4-12B-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             16,
		NumGPUs:          1,
		StartupTimeout:   10 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 131072,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Lumimaid-Magnum-v4-12B-Q6_K.gguf",
		Reasoning:        false,
		Vision:           false,
		ToolCalling:      false,
	},
	{
		Name:             "qwen35-35b-a3b-abliterated-gguf",
		Alias:            "rude-pro",
		HFRepo:           "mradermacher/Huihui-Qwen3.5-35B-A3B-abliterated-i1-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          1,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    262144,
		MaxContextLength: 262144,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Huihui-Qwen3.5-35B-A3B-abliterated.i1-Q4_K_M.gguf",
		Reasoning:        true,
		Vision:           true,
		ToolCalling:      true,
	},
}

type StaticModelRepository struct {
	models []*entity.Model
}

var _ repository.ModelRepository = (*StaticModelRepository)(nil)

func NewStaticModelRepository() *StaticModelRepository {
	return &StaticModelRepository{models: defaultModels}
}

func (r *StaticModelRepository) FindByName(name string) (*entity.Model, error) {
	for _, m := range r.models {
		if m.Name == name || m.Alias == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", name)
}

func (r *StaticModelRepository) FindByAlias(alias string) (*entity.Model, error) {
	for _, m := range r.models {
		if m.Alias == alias {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", alias)
}

func (r *StaticModelRepository) FindAll() ([]*entity.Model, error) {
	return r.models, nil
}

func (r *StaticModelRepository) FindByCategory(category entity.ModelCategory) ([]*entity.Model, error) {
	var result []*entity.Model
	for _, m := range r.models {
		if m.Category == category {
			result = append(result, m)
		}
	}
	return result, nil
}
