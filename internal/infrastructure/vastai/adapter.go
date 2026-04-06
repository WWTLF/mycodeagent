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

func (a *Adapter) CreateInstance(offerID int, image string, envVars map[string]string, onstart string) (int, error) {
	resp, err := a.client.CreateInstance(offerID, image, envVars, onstart)
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
