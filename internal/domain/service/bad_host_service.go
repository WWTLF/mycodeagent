package service

import (
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// BadHostService is the read/management surface for the bad-hosts list.
// DeployService owns the *write* path (records a host as bad on deploy failure)
// and the *filter* path (skips bad hosts during offer selection). This service
// exists purely for the CLI's list / clear operations.
type BadHostService struct {
	repo repository.BadHostRepository
}

func NewBadHostService(repo repository.BadHostRepository) *BadHostService {
	return &BadHostService{repo: repo}
}

func (s *BadHostService) List() ([]*entity.BadHost, error) {
	return s.repo.List()
}

func (s *BadHostService) Remove(machineID int) error {
	return s.repo.Delete(machineID)
}

func (s *BadHostService) Clear() error {
	return s.repo.Clear()
}
