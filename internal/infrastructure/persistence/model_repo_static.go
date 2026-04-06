package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

var defaultModels = []*entity.Model{
	{
		Name:        "qwen3-32b-awq",
		Alias:       "coder",
		HFRepo:      "Qwen/Qwen3-32B-AWQ",
		Category:    entity.CategoryCoding,
		VRAM:        32,
		NumGPUs:     1,
		Temperature:    0.1,
		StartupTimeout: 10 * time.Minute,
		VLLMArgs: []string{
			"--max-model-len", "32768",
			"--quantization", "awq_marlin",
			"--dtype", "half",
			"--enable-auto-tool-choice",
			"--tool-call-parser", "hermes",
			"--reasoning-parser", "deepseek_r1",
			"--gpu-memory-utilization", "0.95",
			"--trust-remote-code",
		},
	},
	{
		Name:           "qwen3-vl-32b-instruct-awq",
		Alias:          "coder_vl",
		HFRepo:         "QuantTrio/Qwen3-VL-32B-Instruct-AWQ",
		Category:       entity.CategoryCoding,
		VRAM:           24,
		NumGPUs:        2,
		Temperature:    0.2,
		StartupTimeout: 15 * time.Minute,
		VLLMArgs: []string{
			"--max-model-len", "32768",
			"--quantization", "awq",
			"--dtype", "half",
			"--tensor-parallel-size", "2",
			"--gpu-memory-utilization", "0.90",
			"--trust-remote-code",
		},
	},
	{
		Name:           "qwen25-32b-instruct-awq",
		Alias:          "writer",
		HFRepo:         "Qwen/Qwen2.5-32B-Instruct-AWQ",
		Category:       entity.CategoryFiction,
		VRAM:           24,
		NumGPUs:        2,
		Temperature:    0.7,
		StartupTimeout: 15 * time.Minute,
		VLLMArgs: []string{
			"--max-model-len", "32768",
			"--quantization", "awq",
			"--dtype", "half",
			"--tensor-parallel-size", "2",
			"--gpu-memory-utilization", "0.95",
			"--trust-remote-code",
		},
	},
	{
		Name:           "dolphin-glm-24b",
		Alias:          "rude",
		HFRepo:         "DavidAU/Dolphin-Mistral-GLM-4.7-Flash-24B-Venice-Edition-Thinking-Uncensored",
		Category:       entity.CategoryDolphin,
		VRAM:           24,
		NumGPUs:        2,
		Temperature:    1.1,
		StartupTimeout: 15 * time.Minute,
		VLLMArgs: []string{
			"--max-model-len", "32768",
			"--tensor-parallel-size", "2",
			"--gpu-memory-utilization", "0.95",
			"--trust-remote-code",
		},
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
