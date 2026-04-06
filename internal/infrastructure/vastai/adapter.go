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
	var results []service.OfferResult
	for _, o := range offers {
		if o.Verification == "deverified" {
			continue
		}
		results = append(results, service.OfferResult{
			ID:            o.ID,
			GPUName:       o.GPUName,
			GPUMemory:     o.GPUMemory,
			DPHTotal:      o.DPHTotal,
			MachineID:     o.MachineID,
			AvailVolAskID: o.AvailVolAskID,
			AvailVolSize:  o.AvailVolSize,
		})
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

func (a *Adapter) WaitForInstance(ctx context.Context, instanceID int, volumeID int) (sshHost string, sshPort int, hourlyRate float64, err error) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for i := 1; ; i++ {
		inst, err := a.client.GetInstance(instanceID)
		if err == nil && inst.Verification == "deverified" {
			fmt.Printf("  [%d] Instance deverified — destroying instance %d\n", i, instanceID)
			_ = a.client.DestroyInstance(instanceID)
			return "", 0, 0, fmt.Errorf("instance %d was deverified and has been destroyed", instanceID)
		}
		// Check volume still exists via API
		if err == nil && volumeID > 0 {
			if !a.volumeExists(volumeID) {
				fmt.Printf("  [%d] Volume V.%d no longer exists — destroying instance %d\n", i, volumeID, instanceID)
				_ = a.client.DestroyInstance(instanceID)
				return "", 0, 0, fmt.Errorf("volume V.%d was deleted — destroyed instance %d, retry init", volumeID, instanceID)
			}
		}
		if err == nil && inst.ActualStatus == "running" {
			host := inst.SSHHost
			if host == "" {
				host = inst.PublicIPAddr
			}
			port := inst.GetSSHPort()
			return host, port, inst.DPHTotal, nil
		}
		// Detect terminal failure: instance stopped/exited unexpectedly or has an error message
		if err == nil && (inst.CurState == "stopped" || inst.CurState == "exited" || inst.IntendedStatus == "stopped") {
			msg := inst.StatusMsg
			if msg == "" {
				msg = fmt.Sprintf("cur_state=%s, intended_status=%s", inst.CurState, inst.IntendedStatus)
			}
			return "", 0, 0, fmt.Errorf("instance %d failed to start: %s", instanceID, msg)
		}
		if err == nil {
			status := inst.ActualStatus
			if inst.StatusMsg != "" {
				status = fmt.Sprintf("%s (%s)", status, inst.StatusMsg)
			}
			volStatus := ""
			if volumeID > 0 {
				if a.volumeExists(volumeID) {
					volStatus = fmt.Sprintf(" | volume V.%d: ok", volumeID)
				} else {
					volStatus = fmt.Sprintf(" | volume V.%d: GONE", volumeID)
				}
			}
			fmt.Printf("  [%d] Status: %s%s\n", i, status, volStatus)
		}

		select {
		case <-ctx.Done():
			return "", 0, 0, fmt.Errorf("instance %d did not start: %w", instanceID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (a *Adapter) volumeExists(volumeID int) bool {
	vols, err := a.client.ListVolumes()
	if err != nil {
		return true // assume exists if API call fails
	}
	for _, v := range vols {
		if v.ID == volumeID {
			return true
		}
	}
	return false
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
