package service

import (
	"fmt"
	"strings"

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

	// Extract volume ID from name (e.g. "V.34258398" → 34258398)
	vastaiID := 0
	if strings.HasPrefix(result.VolumeName, "V.") {
		fmt.Sscanf(result.VolumeName[2:], "%d", &vastaiID)
	}
	if vastaiID == 0 {
		return nil, fmt.Errorf("could not parse volume ID from %s", result.VolumeName)
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

// List returns all volumes, cleaning up stale records when the API confirms they're gone.
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
				fmt.Printf("Warning: failed to delete volume from vast.ai: %v (removing local record anyway)\n", err)
			}
			return s.volumes.Delete(id)
		}
	}
	return fmt.Errorf("volume %d not found", id)
}
