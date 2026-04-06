package service

import (
	"fmt"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

const defaultMountPath = "/root/.cache/huggingface"

type VolumeService struct {
	volumes repository.VolumeRepository
	vastai  VastaiProvider
}

func NewVolumeService(
	volumes repository.VolumeRepository,
	vastai VastaiProvider,
) *VolumeService {
	return &VolumeService{
		volumes: volumes,
		vastai:  vastai,
	}
}

// Create searches for the cheapest volume offer, rents it, and saves to DB.
func (s *VolumeService) Create(sizeGB int) (*entity.Volume, error) {
	if sizeGB <= 0 {
		sizeGB = 50
	}

	fmt.Printf("Searching for volume offers (%d GB)...\n", sizeGB)
	offers, err := s.vastai.SearchVolumeOffers(sizeGB)
	if err != nil {
		return nil, fmt.Errorf("search volume offers: %w", err)
	}
	if len(offers) == 0 {
		return nil, fmt.Errorf("no volume offers found")
	}

	offer := offers[0]
	fmt.Printf("Selected: machine %d (%s) at $%.3f/hr\n", offer.MachineID, offer.Location, offer.DPHTotal)

	fmt.Println("Renting volume...")
	result, err := s.vastai.RentVolume(offer.ID, sizeGB)
	if err != nil {
		return nil, fmt.Errorf("rent volume: %w", err)
	}
	fmt.Printf("Volume created: %s\n", result.VolumeName)

	// Look up the actual volume ID from the API (RentVolume only returns the name)
	remoteVols, err := s.vastai.ListVolumes()
	if err != nil {
		return nil, fmt.Errorf("list volumes after rent: %w", err)
	}
	var vastaiID int
	for _, rv := range remoteVols {
		if rv.VolumeName == result.VolumeName {
			vastaiID = rv.ID
			break
		}
	}
	if vastaiID == 0 {
		return nil, fmt.Errorf("volume %s created but not found in API listing", result.VolumeName)
	}

	vol := &entity.Volume{
		VastaiID:   int64(vastaiID),
		VolumeName: result.VolumeName,
		SizeGB:     sizeGB,
		MountPath:  defaultMountPath,
		MachineID:  offer.MachineID,
	}
	if err := s.volumes.Save(vol); err != nil {
		return nil, fmt.Errorf("save volume: %w", err)
	}

	return vol, nil
}

// List returns all volumes from the local DB.
func (s *VolumeService) List() ([]*entity.Volume, error) {
	return s.volumes.FindAll()
}

// Delete removes a volume from vast.ai and local DB.
func (s *VolumeService) Delete(id int64) error {
	vols, err := s.volumes.FindAll()
	if err != nil {
		return err
	}

	for _, vol := range vols {
		if vol.ID == id {
			if err := s.vastai.DeleteVolume(int(vol.VastaiID)); err != nil {
				return fmt.Errorf("delete volume from vast.ai: %w", err)
			}
			return s.volumes.Delete(id)
		}
	}
	return fmt.Errorf("volume %d not found", id)
}
