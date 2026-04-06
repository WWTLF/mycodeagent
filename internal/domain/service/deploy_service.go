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
	ID            int
	GPUName       string
	GPUMemory     float64
	DPHTotal      float64
	MachineID     int
	AvailVolAskID *int    // volume offer on this machine (nil if none)
	AvailVolSize  float64 // available volume size in GB
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
// Unless noVolume is true, a volume is auto-created if none exists.
func (s *DeployService) Deploy(modelName string, noVolume bool) (*entity.Instance, error) {
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
	var volumeID int
	var mountPath string
	if !noVolume {
		volumeID, mountPath = s.ensureVolume(offer)
	}

	// 4. Create instance
	fmt.Println("Creating instance...")
	instanceID, err := s.vastai.CreateInstance(offer.ID, "vllm/vllm-openai:latest", envVars, vllmCmd, volumeID, mountPath)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	// 5. Wait for instance to be running
	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("wait for instance: %w", err)
	}
	fmt.Printf("Instance running: SSH at %s:%d\n", sshHost, sshPort)

	// 6. Wait for SSH
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

	// 7. Save instance now so ps/tunnel info is available even if health check times out
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

	// 8. Wait for vLLM health in a goroutine — stops as soon as healthy or context expires
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
		return nil, fmt.Errorf("startup timed out after %s (tunnel still running at localhost:%d)", timeout, localPort)
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
func (s *DeployService) DeployCreateOnly(modelName string, noVolume bool) (*CreateOnlyResult, error) {
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
	var volumeID int
	var mountPath string
	if !noVolume {
		volumeID, mountPath = s.ensureVolume(offer)
	}

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

// RestartVLLM kills the running vLLM process and restarts it using /tmp/start_vllm.sh.
func (s *DeployService) RestartVLLM(id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	fmt.Printf("Restarting vLLM on instance %d (%s)...\n", inst.ID, inst.ModelName)

	killCmd := "pkill -f 'vllm serve' 2>/dev/null; sleep 2; pkill -9 -f 'vllm serve' 2>/dev/null; sleep 1"
	fmt.Println("Killing vLLM...")
	s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, killCmd)

	startCmd := "nohup bash /tmp/start_vllm.sh 2>&1 | tee /tmp/vllm.log &"
	fmt.Println("Starting vLLM...")
	_, err = s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, startCmd)
	if err != nil {
		return fmt.Errorf("start vLLM: %w", err)
	}

	fmt.Println("vLLM restart initiated. Use 'mycodeagent log -f' to monitor.")
	return nil
}

const defaultVolumeSizeGB = 50

// ensureVolume returns the volume ID and mount path for the given offer's machine.
// If no volume exists on that machine, it creates one using the offer's avail_vol_ask_id.
func (s *DeployService) ensureVolume(offer OfferResult) (int, string) {
	// Check if we already have a volume on this machine
	vols, _ := s.volumes.FindAll()
	for _, vol := range vols {
		if vol.MachineID == offer.MachineID {
			fmt.Printf("Attaching volume %d (%s) at %s\n", vol.ID, vol.VolumeName, vol.MountPath)
			return int(vol.VastaiID), vol.MountPath
		}
	}

	// No volume on this machine — create one
	if offer.AvailVolAskID == nil {
		fmt.Println("Warning: machine has no volume offer, proceeding without volume")
		return 0, ""
	}

	fmt.Printf("Creating volume (%d GB) on machine %d...\n", defaultVolumeSizeGB, offer.MachineID)
	result, err := s.vastai.RentVolume(*offer.AvailVolAskID, defaultVolumeSizeGB)
	if err != nil {
		fmt.Printf("Warning: could not create volume: %v, proceeding without volume\n", err)
		return 0, ""
	}
	fmt.Printf("Volume created: %s\n", result.VolumeName)

	// Look up the actual volume ID from the API
	remoteVols, err := s.vastai.ListVolumes()
	if err != nil {
		fmt.Printf("Warning: could not list volumes: %v, proceeding without volume\n", err)
		return 0, ""
	}
	var vastaiID int
	for _, rv := range remoteVols {
		if rv.VolumeName == result.VolumeName {
			vastaiID = rv.ID
			break
		}
	}
	if vastaiID == 0 {
		fmt.Println("Warning: volume created but not found in API listing, proceeding without volume")
		return 0, ""
	}

	vol := &entity.Volume{
		VastaiID:   int64(vastaiID),
		VolumeName: result.VolumeName,
		SizeGB:     defaultVolumeSizeGB,
		MountPath:  defaultMountPath,
		MachineID:  offer.MachineID,
	}
	if err := s.volumes.Save(vol); err != nil {
		fmt.Printf("Warning: could not save volume locally: %v\n", err)
	}

	fmt.Printf("Attaching volume %d (%s) at %s\n", vol.ID, vol.VolumeName, vol.MountPath)
	return vastaiID, defaultMountPath
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
