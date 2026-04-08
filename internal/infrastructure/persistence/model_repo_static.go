package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

var defaultModels = []*entity.Model{
	{
		// Native long-context Qwen3.5-35B-A3B MoE in AWQ. The MoE architecture
		// (3B active params/token) keeps per-token KV small enough to fit 130k
		// on a 2-GPU rental, and the model ships with 262k native context — no
		// YaRN needed for our 130k target. ~25 GiB weights, so we ask for 32 GB
		// per GPU to leave room for KV cache + CUDA graphs after the split.
		Name:             "qwen3-5-35b-a3b-awq",
		Alias:            "coder",
		HFRepo:           "QuantTrio/Qwen3.5-35B-A3B-AWQ",
		Category:         entity.CategoryCoding,
		VRAM:             32,
		NumGPUs:          2,
		StartupTimeout:   25 * time.Minute,
		ContextLength:    131072,
		MaxContextLength: 262144,
		// Note: no --quantization / --dtype — vLLM auto-detects from the model
		// config.json and upgrades AWQ to awq_marlin (including AWQMarlinMoEMethod
		// for experts) on Ampere+ GPUs.
		// Note: no --swap-space — removed from vLLM's v1 engine (v0.19.0). The
		// QuantTrio model card example still lists it, but `vllm serve` now
		// errors with "unrecognized arguments: --swap-space".
		// Tool parser: qwen3_xml (not qwen3_coder). Both parsers in v0.19.0
		// scan the same <tool_call>/<function=> XML tokens, but qwen3_xml is
		// the one listed in the official vLLM tool_calling.md for the Qwen3-
		// Coder family. qwen3_coder exists in the code but isn't documented.
		VLLMArgs: []string{
			"--enable-auto-tool-choice",
			"--tool-call-parser", "qwen3_xml",
			"--reasoning-parser", "qwen3",
			"--enable-expert-parallel",
			"--gpu-memory-utilization", "0.90",
			"--trust-remote-code",
			"--enable-prefix-caching",
		},
	},
	{
		Name:             "qwen3-vl-32b-instruct-fp8",
		Alias:            "coder_vl",
		HFRepo:           "Qwen/Qwen3-VL-32B-Instruct-FP8",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    32768,
		MaxContextLength: 131072,
		VLLMArgs: []string{
			"--gpu-memory-utilization", "0.90",
			"--trust-remote-code",
		},
	},
	{
		Name:             "qwen25-32b-instruct-awq",
		Alias:            "writer",
		HFRepo:           "Qwen/Qwen2.5-32B-Instruct-AWQ",
		Category:         entity.CategoryFiction,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    32768,
		MaxContextLength: 131072,
		// No --quantization: auto-detect from config.json upgrades AWQ to
		// awq_marlin on Ampere+ GPUs (~1.5-2x faster than plain awq). Forcing
		// "awq" here would BLOCK that upgrade.
		VLLMArgs: []string{
			"--dtype", "half",
			"--gpu-memory-utilization", "0.95",
			"--enable-prefix-caching",
			"--trust-remote-code",
		},
	},
	{
		Name:             "dolphin-glm-24b-gguf",
		Alias:            "rude",
		HFRepo:           "mradermacher/Dolphin-Mistral-GLM-4.7-Flash-24B-Venice-Edition-Thinking-Uncensored-i1-GGUF",
		Category:         entity.CategoryDolphin,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    65536,
		MaxContextLength: 131072,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Dolphin-Mistral-GLM-4.7-Flash-24B-Venice-Edition-Thinking-Uncensored.i1-Q4_K_M.gguf",
	},
	{
		// GGUF fallback for the Qwen3.5-35B-A3B MoE. Uses unsloth's Dynamic 2.0
		// UD-Q4_K_XL quant rather than the lmstudio-community Q4_K_M: per-layer
		// bit allocation calibrated on a dataset, so the router weights and
		// sensitive attention projections get more bits at the same ~22 GB
		// footprint. Measurably better quality than vanilla Q4_K_M for MoE.
		// ContextLength matches the AWQ `coder` floor — Mamba hybrid layers
		// keep per-token KV cheap enough to hold 130k on 2x 24 GB.
		Name:             "qwen3-5-35b-a3b-gguf",
		Alias:            "coder-2",
		HFRepo:           "unsloth/Qwen3.5-35B-A3B-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		ContextLength:    131072,
		MaxContextLength: 262144,
		StartupTimeout:   15 * time.Minute,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Qwen3.5-35B-A3B-UD-Q4_K_XL.gguf",
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
