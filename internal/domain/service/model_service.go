package service

import (
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// ModelService is a thin wrapper around the model repository so the App layer
// can satisfy the "no direct repo access" rule. The static catalog is in-memory
// so these methods don't need a context — if the repo ever becomes I/O-backed,
// signatures change once and the compiler finds every caller.
type ModelService struct {
	models repository.ModelRepository
}

func NewModelService(models repository.ModelRepository) *ModelService {
	return &ModelService{models: models}
}

func (s *ModelService) List() ([]*entity.Model, error) {
	return s.models.FindAll()
}

func (s *ModelService) FindByName(name string) (*entity.Model, error) {
	return s.models.FindByName(name)
}

func (s *ModelService) FindByAlias(alias string) (*entity.Model, error) {
	return s.models.FindByAlias(alias)
}
