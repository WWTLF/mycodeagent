package service

import (
	"context"
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// VastaiProvider abstracts vast.ai API operations for the domain layer.
type VastaiProvider interface {
	SearchOffers(minGPURAM int, numGPUs int) ([]OfferResult, error)
	CreateInstance(offerID int, image string, envVars map[string]string, onstart string, volumeID int, mountPath string) (instanceID int, err error)
	WaitForInstance(ctx context.Context, instanceID int) (sshHost string, sshPort int, hourlyRate float64, err error)
	StopInstance(instanceID int) error
	DestroyInstance(instanceID int) error
	SearchVolumeOffers(sizeGB int) ([]VolumeOfferResult, error)
	RentVolume(offerID int, sizeGB int) (*VolumeResult, error)
	ListVolumes() ([]VolumeResult, error)
	DeleteVolume(volumeID int) error
}

type VolumeOfferResult struct {
	ID        int
	MachineID int
	Location  string
	DPHTotal  float64
}

type VolumeResult struct {
	ID         int
	VolumeName string
	SizeGB     int
	MachineID  int
}

// SSHTunnelProvider abstracts SSH tunnel operations for the domain layer.
type SSHTunnelProvider interface {
	StartTunnel(localPort int, sshHost string, sshPort int) (pid int, err error)
	StopTunnel(pid int) error
	WaitForSSH(ctx context.Context, host string, port int) error
	RunRemoteCommand(sshHost string, sshPort int, command string) ([]byte, error)
	FindFreePort(basePort int) (int, error)
	WaitForVLLMHealth(ctx context.Context, localPort int) error
}

type OfferResult struct {
	ID        int
	GPUName   string
	GPUMemory float64
	DPHTotal  float64
}

type DeployService struct {
	models    repository.ModelRepository
	instances repository.InstanceRepository
	volumes   repository.VolumeRepository
	vastai    VastaiProvider
	ssh       SSHTunnelProvider
	basePort  int
	hfToken   string
}

func NewDeployService(
	models repository.ModelRepository,
	instances repository.InstanceRepository,
	volumes repository.VolumeRepository,
	vastai VastaiProvider,
	ssh SSHTunnelProvider,
	basePort int,
	hfToken string,
) *DeployService {
	return &DeployService{
		models:    models,
		instances: instances,
		volumes:   volumes,
		vastai:    vastai,
		ssh:       ssh,
		basePort:  basePort,
		hfToken:   hfToken,
	}
}

// Deploy executes the full init flow: find offer → create instance → SSH → vLLM → tunnel.
// If volumeDBID > 0, uses that specific volume; otherwise auto-selects the first available.
func (s *DeployService) Deploy(modelName string, volumeDBID int64) (*entity.Instance, error) {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return nil, err
	}

	// Use model's startup timeout, default to 10 minutes
	timeout := model.StartupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Printf("Startup timeout: %s\n", timeout)

	// 1. Find cheapest offer
	numGPUs := model.NumGPUs
	if numGPUs <= 0 {
		numGPUs = 1
	}
	fmt.Printf("Searching for %dx GPU with >= %dGB VRAM...\n", numGPUs, model.VRAM)
	offers, err := s.vastai.SearchOffers(model.VRAM, numGPUs)
	if err != nil {
		return nil, fmt.Errorf("search offers: %w", err)
	}
	if len(offers) == 0 {
		return nil, fmt.Errorf("no GPU offers found with %dx >= %dGB VRAM", numGPUs, model.VRAM)
	}
	offer := offers[0] // cheapest (already sorted)
	fmt.Printf("Selected: %s (%.0fGB) at $%.3f/hr\n", offer.GPUName, offer.GPUMemory, offer.DPHTotal)

	// 2. Build vLLM startup command
	vllmCmd := s.buildVLLMCommand(model)

	envVars := map[string]string{}
	if s.hfToken != "" {
		envVars["HF_TOKEN"] = s.hfToken
		envVars["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}

	// 3. Check for volume to attach
	volumeID, mountPath := s.activeVolume(volumeDBID)

	// 4. Create instance
	fmt.Println("Creating instance...")
	instanceID, err := s.vastai.CreateInstance(offer.ID, "vllm/vllm-openai:latest", envVars, vllmCmd, volumeID, mountPath)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	// 4. Wait for instance to be running
	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("wait for instance: %w", err)
	}
	fmt.Printf("Instance running: SSH at %s:%d\n", sshHost, sshPort)

	// 5. Wait for SSH
	fmt.Println("Waiting for SSH...")
	if err := s.ssh.WaitForSSH(ctx, sshHost, sshPort); err != nil {
		return nil, fmt.Errorf("wait for SSH: %w", err)
	}

	// 6. Allocate local port and start tunnel
	localPort, err := s.ssh.FindFreePort(s.basePort)
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}

	fmt.Printf("Starting SSH tunnel on port %d...\n", localPort)
	tunnelPID, err := s.ssh.StartTunnel(localPort, sshHost, sshPort)
	if err != nil {
		return nil, fmt.Errorf("start tunnel: %w", err)
	}

	// 7. Wait for vLLM health in a goroutine — stops as soon as healthy or context expires
	fmt.Println("Waiting for vLLM to become healthy (model downloading, this may take a while)...")
	healthCh := make(chan error, 1)
	go func() {
		healthCh <- s.ssh.WaitForVLLMHealth(ctx, localPort)
	}()

	select {
	case err := <-healthCh:
		if err != nil {
			return nil, fmt.Errorf("vLLM health check: %w", err)
		}
	case <-ctx.Done():
		return nil, fmt.Errorf("startup timed out after %s", timeout)
	}

	// 8. Save instance
	inst := &entity.Instance{
		VastaiID:   int64(instanceID),
		ModelName:  model.Name,
		Status:     entity.StatusRunning,
		LocalPort:  localPort,
		SSHHost:    sshHost,
		SSHPort:    sshPort,
		TunnelPID:  tunnelPID,
		HourlyRate: hourlyRate,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	fmt.Printf("\nAPI available at: http://localhost:%d/v1\n", localPort)
	return inst, nil
}

type CreateOnlyResult struct {
	Instance   *entity.Instance
	VLLMCommand string
}

// DeployCreateOnly creates the instance and waits for it to be running, but does not
// set up the SSH tunnel or wait for vLLM health. Returns the instance with SSH details.
func (s *DeployService) DeployCreateOnly(modelName string, volumeDBID int64) (*CreateOnlyResult, error) {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return nil, err
	}

	timeout := model.StartupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 1. Find cheapest offer
	numGPUs := model.NumGPUs
	if numGPUs <= 0 {
		numGPUs = 1
	}
	fmt.Printf("Searching for %dx GPU with >= %dGB VRAM...\n", numGPUs, model.VRAM)
	offers, err := s.vastai.SearchOffers(model.VRAM, numGPUs)
	if err != nil {
		return nil, fmt.Errorf("search offers: %w", err)
	}
	if len(offers) == 0 {
		return nil, fmt.Errorf("no GPU offers found with %dx >= %dGB VRAM", numGPUs, model.VRAM)
	}
	offer := offers[0]
	fmt.Printf("Selected: %s (%.0fGB) at $%.3f/hr\n", offer.GPUName, offer.GPUMemory, offer.DPHTotal)

	// 2. Build vLLM startup command
	vllmCmd := s.buildVLLMCommand(model)

	envVars := map[string]string{}
	if s.hfToken != "" {
		envVars["HF_TOKEN"] = s.hfToken
		envVars["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}

	// 3. Check for volume to attach
	volumeID, mountPath := s.activeVolume(volumeDBID)

	// 4. Create instance
	fmt.Println("Creating instance...")
	instanceID, err := s.vastai.CreateInstance(offer.ID, "vllm/vllm-openai:latest", envVars, vllmCmd, volumeID, mountPath)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	// 4. Wait for instance to be running
	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("wait for instance: %w", err)
	}

	// 5. Save instance (no tunnel, no health check)
	inst := &entity.Instance{
		VastaiID:   int64(instanceID),
		ModelName:  model.Name,
		Status:     entity.StatusRunning,
		SSHHost:    sshHost,
		SSHPort:    sshPort,
		HourlyRate: hourlyRate,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	// Build the raw vLLM command (without the onstart wrapper)
	rawCmd := fmt.Sprintf("vllm serve '%s' --host 0.0.0.0 --port 8000", model.HFRepo)
	for _, arg := range model.VLLMArgs {
		rawCmd += " " + arg
	}

	return &CreateOnlyResult{Instance: inst, VLLMCommand: rawCmd}, nil
}

// Stop stops a single instance by local DB ID.
func (s *DeployService) Stop(id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	s.ssh.StopTunnel(inst.TunnelPID)

	if err := s.vastai.StopInstance(int(inst.VastaiID)); err != nil {
		return fmt.Errorf("stop vast.ai instance: %w", err)
	}

	inst.Status = entity.StatusStopped
	inst.TunnelPID = 0
	return s.instances.Update(inst)
}

// Destroy destroys a single instance permanently.
func (s *DeployService) Destroy(id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	s.ssh.StopTunnel(inst.TunnelPID)

	if err := s.vastai.DestroyInstance(int(inst.VastaiID)); err != nil {
		return fmt.Errorf("destroy vast.ai instance: %w", err)
	}

	return s.instances.Delete(inst.ID)
}

// activeVolume returns the volume ID and mount path.
// If volumeDBID > 0, uses that specific volume; otherwise picks the first available.
func (s *DeployService) activeVolume(volumeDBID int64) (int, string) {
	vols, err := s.volumes.FindAll()
	if err != nil || len(vols) == 0 {
		return 0, ""
	}

	var vol *entity.Volume
	if volumeDBID > 0 {
		for _, v := range vols {
			if v.ID == volumeDBID {
				vol = v
				break
			}
		}
		if vol == nil {
			fmt.Printf("Warning: volume %d not found, skipping\n", volumeDBID)
			return 0, ""
		}
	} else {
		vol = vols[0]
	}

	fmt.Printf("Attaching volume %s (ID %d) at %s\n", vol.VolumeName, vol.ID, vol.MountPath)
	return int(vol.VastaiID), vol.MountPath
}

func (s *DeployService) buildVLLMCommand(model *entity.Model) string {
	vllmCmd := fmt.Sprintf("vllm serve '%s' --host 0.0.0.0 --port 8000", model.HFRepo)
	for _, arg := range model.VLLMArgs {
		vllmCmd += " " + arg
	}
	// Write as a script to avoid shell line-splitting issues in vast.ai onstart
	script := fmt.Sprintf("echo '%s' > /tmp/start_vllm.sh && chmod +x /tmp/start_vllm.sh && bash /tmp/start_vllm.sh 2>&1 | tee /tmp/vllm.log", vllmCmd)
	return script
}
