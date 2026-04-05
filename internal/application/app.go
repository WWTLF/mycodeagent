package application

import (
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

type App struct {
	Models    repository.ModelRepository
	Instances repository.InstanceRepository
}
