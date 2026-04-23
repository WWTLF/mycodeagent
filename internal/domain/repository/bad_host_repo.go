package repository

import "github.com/WWTLF/mycodeagent/internal/domain/entity"

// BadHostRepository tracks vast.ai machine IDs that have failed to provision cleanly.
// The deploy flow consults IsBad to skip them in offer search, and records new
// failures via Add when WaitForInstance times out.
type BadHostRepository interface {
	Add(machineID int, reason string) error
	List() ([]*entity.BadHost, error)
	IsBad(machineID int) (bool, error)
	Delete(machineID int) error
	Clear() error
}
