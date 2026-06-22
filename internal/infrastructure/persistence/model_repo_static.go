package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// All models run on vLLM (the only engine). Every HFRepo below was verified to
// exist on HuggingFace and to be a vLLM-servable quant (AWQ or FP8) — not GGUF.
// Contexts are kept conservative to guarantee the server boots; scaledContextLength
// grows ContextLength toward MaxContextLength when a fatter GPU is rented.
//
// FP8 note: the FP8 checkpoints run on Ampere (RTX 3090) via vLLM's fp8-marlin
// W8A16 path; native FP8 compute needs Ada/Hopper (RTX 4090/L40/H100), where
// they're faster. Either way they load on the compute_cap>=8.0 hosts we filter for.
var defaultModels = []*entity.Model{
	{
		Name:             "qwen3-8b-awq",
		Alias:            "coder-mini",
		HFRepo:           "Qwen/Qwen3-8B-AWQ",
		Category:         entity.CategoryCoding,
		VRAM:             16,
		NumGPUs:          1,
		DiskGB:           30, // ~6 GB AWQ weights + scratch
		StartupTimeout:   10 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 131072,
		VLLMArgs: []string{
			// Auto-detect upgrades AWQ → awq_marlin on Ampere+ — don't force --quantization.
			"--dtype", "half",
			"--gpu-memory-utilization", "0.90",
			// fp8 KV halves cache memory: ~6 GB weights + ~4.7 GB KV @131k fits in 16 GB.
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
		Name:             "qwen3-30b-a3b-thinking-fp8",
		Alias:            "coder",
		HFRepo:           "Qwen/Qwen3-30B-A3B-Thinking-2507-FP8",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2, // ~30 GB FP8 weights need 2x24 GB with expert-parallel
		DiskGB:           60,
		StartupTimeout:   20 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 262144, // native 262k — no YaRN needed
		VLLMArgs: []string{
			"--gpu-memory-utilization", "0.90",
			// MoE: shard experts across both ranks instead of replicating (fits 30 GB on 2x24).
			"--enable-expert-parallel",
			// fp8 KV keeps 131k+ in the remaining headroom.
			"--kv-cache-dtype", "fp8",
			"--enable-prefix-caching",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "qwen3_xml",
			"--reasoning-parser", "qwen3",
			"--trust-remote-code",
		},
		Reasoning:   true, // dedicated thinking model
		Vision:      false,
		ToolCalling: true,
	},
	{
		Name:             "qwen3-coder-30b-a3b-fp8",
		Alias:            "coder-hq",
		HFRepo:           "Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		DiskGB:           60,
		StartupTimeout:   20 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 262144,
		VLLMArgs: []string{
			"--gpu-memory-utilization", "0.90",
			"--enable-expert-parallel",
			"--kv-cache-dtype", "fp8",
			"--enable-prefix-caching",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "qwen3_xml",
			"--trust-remote-code",
		},
		Reasoning:   false, // Qwen3-Coder instruct is non-thinking
		Vision:      false,
		ToolCalling: true,
	},
	{
		Name:             "qwen25-32b-instruct-awq",
		Alias:            "writer",
		HFRepo:           "Qwen/Qwen2.5-32B-Instruct-AWQ",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2, // ~20 GB AWQ weights; 2x24 leaves room for long-form KV
		DiskGB:           45,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    32768,  // native 32k
		MaxContextLength: 131072, // YaRN extends to 128k
		VLLMArgs: []string{
			"--dtype", "half", // some Ampere AWQ kernels need fp16, not bf16
			"--gpu-memory-utilization", "0.95",
			"--enable-prefix-caching",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "hermes",
			// YaRN so scaledContextLength can grow past native 32k on bigger offers.
			"--rope-scaling", `'{"rope_type":"yarn","factor":4.0,"original_max_position_embeddings":32768}'`,
			"--trust-remote-code",
		},
		Reasoning:   false,
		Vision:      false,
		ToolCalling: true,
	},
	{
		Name:             "dolphin-29-llama3-8b-awq",
		Alias:            "rude",
		HFRepo:           "solidrust/dolphin-2.9-llama3-8b-AWQ",
		Category:         entity.CategoryCoding,
		VRAM:             16,
		NumGPUs:          1,
		DiskGB:           30,
		StartupTimeout:   10 * time.Minute,
		ContextLength:    8192, // Llama-3-8B native 8k
		MaxContextLength: 8192,
		VLLMArgs: []string{
			"--dtype", "half",
			"--gpu-memory-utilization", "0.90",
			"--enable-prefix-caching",
			"--trust-remote-code",
		},
		Reasoning:   false,
		Vision:      false,
		ToolCalling: false,
	},
	{
		Name:             "qwen3-30b-a3b-abliterated-fp8",
		Alias:            "rude-pro",
		HFRepo:           "hotpizzatactics/Qwen3-30B-A3B-abliterated-FP8-dynamic",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		DiskGB:           60,
		StartupTimeout:   20 * time.Minute,
		ContextLength:    32768, // conservative: community quant, keep boot guaranteed
		MaxContextLength: 32768,
		VLLMArgs: []string{
			"--gpu-memory-utilization", "0.90",
			"--enable-expert-parallel",
			"--enable-prefix-caching",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "qwen3_xml",
			"--reasoning-parser", "qwen3",
			"--trust-remote-code",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
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
