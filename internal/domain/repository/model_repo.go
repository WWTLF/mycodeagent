package repository

import "github.com/WWTLF/mycodeagent/internal/domain/entity"

type ModelRepository interface {
	FindByName(name string) (*entity.Model, error)
	FindByAlias(alias string) (*entity.Model, error)
	FindAll() ([]*entity.Model, error)
	FindByCategory(category entity.ModelCategory) ([]*entity.Model, error)
}
