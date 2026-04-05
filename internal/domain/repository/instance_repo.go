package repository

import "github.com/WWTLF/mycodeagent/internal/domain/entity"

type InstanceRepository interface {
	Save(instance *entity.Instance) error
	FindByID(id int64) (*entity.Instance, error)
	FindByVastaiID(vastaiID int64) (*entity.Instance, error)
	FindAll() ([]*entity.Instance, error)
	FindRunning() ([]*entity.Instance, error)
	Update(instance *entity.Instance) error
	Delete(id int64) error
}
