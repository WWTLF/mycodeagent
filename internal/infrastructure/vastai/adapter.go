package vastai

import (
	"context"
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/service"
)

// Adapter wraps Client to implement service.VastaiProvider.
type Adapter struct {
	client *Client
}

var _ service.VastaiProvider = (*Adapter)(nil)

func NewAdapter(apiKey string) *Adapter {
	return &Adapter{client: NewClient(apiKey)}
}

func (a *Adapter) SetVerbose(v bool) {
	a.client.SetVerbose(v)
}

func (a *Adapter) SearchOffers(minGPURAM int, numGPUs int) ([]service.OfferResult, error) {
	offers, err := a.client.SearchOffers(minGPURAM, numGPUs)
	if err != nil {
		return nil, err
	}
	results := make([]service.OfferResult, len(offers))
	for i, o := range offers {
		results[i] = service.OfferResult{
			ID:        o.ID,
			GPUName:   o.GPUName,
			GPUMemory: o.GPUMemory,
			DPHTotal:  o.DPHTotal,
		}
	}
	return results, nil
}

func (a *Adapter) CreateInstance(offerID int, image string, envVars map[string]string, onstart string, volumeID int, mountPath string) (int, error) {
	resp, err := a.client.CreateInstance(offerID, image, envVars, onstart, volumeID, mountPath)
	if err != nil {
		return 0, err
	}
	if !resp.Success {
		return 0, fmt.Errorf("create instance failed")
	}
	id, err := resp.NewContract.Int64()
	if err != nil {
		return 0, fmt.Errorf("parse instance ID %q: %w", resp.NewContract, err)
	}
	return int(id), nil
}

func (a *Adapter) WaitForInstance(ctx context.Context, instanceID int) (sshHost string, sshPort int, hourlyRate float64, err error) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for i := 1; ; i++ {
		inst, err := a.client.GetInstance(instanceID)
		if err == nil && inst.ActualStatus == "running" {
			host := inst.SSHHost
			if host == "" {
				host = inst.PublicIPAddr
			}
			port := inst.GetSSHPort()
			return host, port, inst.DPHTotal, nil
		}
		if err == nil {
			fmt.Printf("  [%d] Status: %s\n", i, inst.ActualStatus)
		}

		select {
		case <-ctx.Done():
			return "", 0, 0, fmt.Errorf("instance %d did not start: %w", instanceID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (a *Adapter) StopInstance(instanceID int) error {
	return a.client.StopInstance(instanceID)
}

func (a *Adapter) DestroyInstance(instanceID int) error {
	return a.client.DestroyInstance(instanceID)
}

func (a *Adapter) SearchVolumeOffers(sizeGB int) ([]service.VolumeOfferResult, error) {
	offers, err := a.client.SearchVolumeOffers(sizeGB)
	if err != nil {
		return nil, err
	}
	results := make([]service.VolumeOfferResult, len(offers))
	for i, o := range offers {
		results[i] = service.VolumeOfferResult{
			ID:        o.ID,
			MachineID: o.MachineID,
			Location:  o.Location,
			DPHTotal:  o.DPHTotal,
		}
	}
	return results, nil
}

func (a *Adapter) RentVolume(offerID int, sizeGB int) (*service.VolumeResult, error) {
	resp, err := a.client.RentVolume(offerID, sizeGB)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("rent volume failed")
	}
	return &service.VolumeResult{
		VolumeName: resp.VolumeName,
	}, nil
}

func (a *Adapter) ListVolumes() ([]service.VolumeResult, error) {
	volumes, err := a.client.ListVolumes()
	if err != nil {
		return nil, err
	}
	results := make([]service.VolumeResult, len(volumes))
	for i, v := range volumes {
		name := v.Label
		if name == "" {
			name = fmt.Sprintf("V.%d", v.ID)
		}
		results[i] = service.VolumeResult{
			ID:         v.ID,
			VolumeName: name,
			SizeGB:     int(v.DiskSpace),
			MachineID:  v.MachineID,
		}
	}
	return results, nil
}

func (a *Adapter) DeleteVolume(volumeID int) error {
	return a.client.DeleteVolume(volumeID)
}
