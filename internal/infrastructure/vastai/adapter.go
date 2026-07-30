package vastai

import (
	"context"
	"fmt"
	"io"
	"net/http"
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

func (a *Adapter) SearchOffers(minGPURAM int, numGPUs int, minDiskGB int) ([]service.OfferResult, error) {
	offers, err := a.client.SearchOffers(minGPURAM, numGPUs, minDiskGB)
	if err != nil {
		return nil, err
	}
	var results []service.OfferResult
	for _, o := range offers {
		// Skip anything that isn't explicitly verified. Unverified hosts often fail
		// at container creation with `docker_build() error writing dockerfile`.
		if o.Verification != "verified" {
			continue
		}
		results = append(results, service.OfferResult{
			ID:        o.ID,
			GPUName:   o.GPUName,
			NumGPUs:   o.NumGPUs,
			GPUMemory: o.GPUMemory,
			DPHTotal:  o.DPHTotal,
			MachineID: o.MachineID,
		})
	}
	return results, nil
}

func (a *Adapter) CreateInstance(offerID int, image string, envVars map[string]string, onstart string, diskGB int) (int, error) {
	resp, err := a.client.CreateInstance(offerID, image, envVars, onstart, diskGB)
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
		if err == nil && inst.Verification == "deverified" {
			fmt.Printf("  [%d] Instance deverified — destroying instance %d\n", i, instanceID)
			_ = a.client.DestroyInstance(instanceID)
			return "", 0, 0, fmt.Errorf("instance %d was deverified and has been destroyed", instanceID)
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
			fmt.Printf("  [%d] Status: %s\n", i, status)
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

func (a *Adapter) StartInstance(instanceID int) error {
	return a.client.StartInstance(instanceID)
}

func (a *Adapter) DestroyInstance(instanceID int) error {
	return a.client.DestroyInstance(instanceID)
}

// GetInstance fetches a single instance and maps it to a domain DTO.
func (a *Adapter) GetInstance(ctx context.Context, vastaiID int) (*service.RemoteInstance, error) {
	info, err := a.client.GetInstance(vastaiID)
	if err != nil {
		return nil, err
	}
	r := mapRemoteInstance(info)
	return &r, nil
}

// ListRemoteInstances fetches all user instances and maps them to domain DTOs.
func (a *Adapter) ListRemoteInstances(ctx context.Context) ([]*service.RemoteInstance, error) {
	infos, err := a.client.ListInstances()
	if err != nil {
		return nil, err
	}
	results := make([]*service.RemoteInstance, len(infos))
	for i := range infos {
		r := mapRemoteInstance(&infos[i])
		results[i] = &r
	}
	return results, nil
}

// GetInstanceLogs requests log generation from vast.ai, then GETs the resulting
// S3 URL with retry. The vast.ai API returns the URL synchronously but the upload
// itself takes a few seconds; this method waits until the URL serves a 200 or
// gives up after a bounded number of retries. Returns the raw log bytes.
func (a *Adapter) GetInstanceLogs(ctx context.Context, vastaiID int, tail string) ([]byte, error) {
	logURL, err := a.client.GetInstanceLogs(vastaiID, tail)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	const maxAttempts = 10
	const retryDelay = 2 * time.Second
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch log URL: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			return io.ReadAll(resp.Body)
		}
		resp.Body.Close()
	}
	return nil, fmt.Errorf("logs not available after %d retries", maxAttempts)
}

// clientFor returns the configured client when apiKey is empty, or a one-shot
// client constructed for the supplied key. Used by the credential methods so
// the login flow can verify a freshly-typed key before it's persisted.
func (a *Adapter) clientFor(apiKey string) *Client {
	if apiKey == "" {
		return a.client
	}
	return NewClient(apiKey)
}

// VerifyAPIKey checks an API key by hitting the auth endpoint.
func (a *Adapter) VerifyAPIKey(ctx context.Context, apiKey string) error {
	return a.clientFor(apiKey).VerifyAPIKey()
}

// ListSSHKeys returns the public_key strings registered on the vast.ai account.
func (a *Adapter) ListSSHKeys(ctx context.Context, apiKey string) ([]string, error) {
	rawKeys, err := a.clientFor(apiKey).ListSSHKeys()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rawKeys))
	for _, k := range rawKeys {
		if pk, ok := k["public_key"].(string); ok {
			keys = append(keys, pk)
		}
	}
	return keys, nil
}

// CreateSSHKey uploads a public SSH key to the vast.ai account.
func (a *Adapter) CreateSSHKey(ctx context.Context, apiKey string, pubKey string) error {
	return a.clientFor(apiKey).CreateSSHKey(pubKey)
}

// mapRemoteInstance translates vastai.InstanceInfo into the domain DTO.
func mapRemoteInstance(i *InstanceInfo) service.RemoteInstance {
	return service.RemoteInstance{
		VastaiID:     i.ID,
		ActualStatus: i.ActualStatus,
		CurState:     i.CurState,
		StatusMsg:    i.StatusMsg,
		SSHHost:      i.SSHHost,
		PublicIPAddr: i.PublicIPAddr,
		SSHPort:      i.GetSSHPort(),
		HourlyRate:   i.DPHTotal,
		Onstart:      i.Onstart,
	}
}
