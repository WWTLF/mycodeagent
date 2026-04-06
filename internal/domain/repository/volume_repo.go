package repository

import "github.com/WWTLF/mycodeagent/internal/domain/entity"

type VolumeRepository interface {
	Save(volume *entity.Volume) error
	FindAll() ([]*entity.Volume, error)
	Delete(id int64) error
}
