package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

var defaultModels = []*entity.Model{
	{
		Name:             "qwen35-9b-gguf",
		Alias:            "coder-mini",
		HFRepo:           "lmstudio-community/Qwen3.5-9B-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             16,
		NumGPUs:          1,
		StartupTimeout:   10 * time.Minute,
		ContextLength:    262144,
		MaxContextLength: 262144,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Qwen3.5-9B-Q4_K_M.gguf",
		Reasoning:        true,
		Vision:           true,
		ToolCalling:      true,
	},
	{
		Name:             "qwen35-35b-a3b-gguf",
		Alias:            "coder",
		HFRepo:           "lmstudio-community/Qwen3.5-35B-A3B-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             32,
		NumGPUs:          1,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    262144,
		MaxContextLength: 262144,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Qwen3.5-35B-A3B-Q4_K_M.gguf",
		Reasoning:        true,
		Vision:           true,
		ToolCalling:      true,
	},
	{
		Name:             "qwen35-27b-gguf",
		Alias:            "coder-vision",
		HFRepo:           "lmstudio-community/Qwen3.5-27B-GGUF",
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          2,
		StartupTimeout:   15 * time.Minute,
		ContextLength:    262144,
		MaxContextLength: 262144,
		Engine:           entity.EngineLMStudio,
		GGUFFile:         "Qwen3.5-27B-Q6_K.gguf",
		Reasoning:        true,
		Vision:           true,
		ToolCalling:      true,
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
