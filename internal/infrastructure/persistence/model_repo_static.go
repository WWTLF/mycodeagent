package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

var defaultModels = []*entity.Model{
	{
		Name:             "qwen3-5-35b-a3b-gptq",
		Alias:            "coder",
		HFRepo:           "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   20 * time.Minute,
		ContextLength:    32768,
		MaxContextLength: 131072,
		VLLMArgs: []string{
			"--quantization", "gptq_marlin",
			"--dtype", "half",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "hermes",
			"--reasoning-parser", "deepseek_r1",
			"--gpu-memory-utilization", "0.95",
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
		VLLMArgs: []string{
			"--quantization", "awq",
			"--dtype", "half",
			"--gpu-memory-utilization", "0.95",
			"--trust-remote-code",
		},
	},
	{
		Name:             "dolphin-glm-24b",
		Alias:            "rude",
		HFRepo:           "DavidAU/Dolphin-Mistral-GLM-4.7-Flash-24B-Venice-Edition-Thinking-Uncensored",
		Category:         entity.CategoryDolphin,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    32768,
		MaxContextLength: 131072,
		VLLMArgs: []string{
			"--gpu-memory-utilization", "0.95",
			"--trust-remote-code",
		},
	},
	{
		Name:             "qwen3-5-35b-a3b-gguf",
		Alias:            "coder-2",
		HFRepo:           "lmstudio-community/Qwen3.5-35B-A3B-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		ContextLength:    32768,
		MaxContextLength: 131072,
		StartupTimeout:   15 * time.Minute,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Qwen3.5-35B-A3B-Q4_K_M.gguf",
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
