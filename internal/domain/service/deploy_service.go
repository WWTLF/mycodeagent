package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// VastaiProvider abstracts vast.ai API operations for the domain layer.
type VastaiProvider interface {
	SearchOffers(minGPURAM int, numGPUs int) ([]OfferResult, error)
	CreateInstance(offerID int, image string, envVars map[string]string, onstart string, volumeID int, mountPath string) (instanceID int, err error)
	WaitForInstance(ctx context.Context, instanceID int, volumeID int) (sshHost string, sshPort int, hourlyRate float64, err error)
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

// EngineProvider abstracts engine-specific deployment details (vLLM, LM Studio, etc.)
type EngineProvider interface {
	// DockerImage returns the Docker image to use on vast.ai
	DockerImage() string
	// BuildOnstart returns the onstart shell script for the instance
	BuildOnstart(model *entity.Model, hfToken string) string
	// BuildRawCommand returns a human-readable command for --create-instance-only output
	BuildRawCommand(model *entity.Model) string
	// VolumeMountPath returns where to mount the persistent volume
	VolumeMountPath() string
	// RestartCommands returns the kill and start commands for restarting the server
	RestartCommands(model *entity.Model) (killCmd string, startCmd string)
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
	engines   map[entity.ModelEngine]EngineProvider
	basePort  int
	hfToken   string
}

func NewDeployService(
	models repository.ModelRepository,
	instances repository.InstanceRepository,
	volumes repository.VolumeRepository,
	vastai VastaiProvider,
	ssh SSHTunnelProvider,
	engines map[entity.ModelEngine]EngineProvider,
	basePort int,
	hfToken string,
) *DeployService {
	return &DeployService{
		models:    models,
		instances: instances,
		volumes:   volumes,
		vastai:    vastai,
		ssh:       ssh,
		engines:   engines,
		basePort:  basePort,
		hfToken:   hfToken,
	}
}

// engineFor returns the EngineProvider for the given model, defaulting to vLLM.
func (s *DeployService) engineFor(model *entity.Model) EngineProvider {
	eng := model.Engine
	if eng == "" {
		eng = entity.EngineVLLM
	}
	return s.engines[eng]
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

	// 2. Build startup command via engine provider
	engine := s.engineFor(model)
	onstart := engine.BuildOnstart(model, s.hfToken)

	envVars := map[string]string{}
	if s.hfToken != "" {
		envVars["HF_TOKEN"] = s.hfToken
		envVars["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}

	// 3. Ensure volume exists
	var volumeID int
	var mountPath string
	if !noVolume {
		volumeID, mountPath, err = s.ensureVolume(offer, engine.VolumeMountPath())
		if err != nil {
			return nil, err
		}
	}

	// 4. Create instance
	fmt.Println("Creating instance...")
	instanceID, err := s.vastai.CreateInstance(offer.ID, engine.DockerImage(), envVars, onstart, volumeID, mountPath)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	// 5. Wait for instance to be running
	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID, volumeID)
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
		VolumeID:   int64(volumeID),
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
	Instance     *entity.Instance
	ServeCommand string
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

	// 2. Build startup command via engine provider
	engine := s.engineFor(model)
	onstart := engine.BuildOnstart(model, s.hfToken)

	envVars := map[string]string{}
	if s.hfToken != "" {
		envVars["HF_TOKEN"] = s.hfToken
		envVars["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}

	// 3. Ensure volume exists
	var volumeID int
	var mountPath string
	if !noVolume {
		volumeID, mountPath, err = s.ensureVolume(offer, engine.VolumeMountPath())
		if err != nil {
			return nil, err
		}
	}

	// 4. Create instance
	fmt.Println("Creating instance...")
	instanceID, err := s.vastai.CreateInstance(offer.ID, engine.DockerImage(), envVars, onstart, volumeID, mountPath)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	// 5. Wait for instance to be running
	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID, volumeID)
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
		VolumeID:   int64(volumeID),
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	return &CreateOnlyResult{Instance: inst, ServeCommand: engine.BuildRawCommand(model)}, nil
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

// Destroy destroys a single instance and its volumes permanently.
func (s *DeployService) Destroy(id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	s.ssh.StopTunnel(inst.TunnelPID)

	if err := s.vastai.DestroyInstance(int(inst.VastaiID)); err != nil {
		return fmt.Errorf("destroy vast.ai instance: %w", err)
	}

	// Delete the associated volume
	if inst.VolumeID > 0 {
		vols, _ := s.volumes.FindAll()
		for _, vol := range vols {
			if vol.VastaiID == inst.VolumeID {
				fmt.Printf("Deleting volume %d (%s)...\n", vol.ID, vol.VolumeName)
				if err := s.vastai.DeleteVolume(int(vol.VastaiID)); err != nil {
					fmt.Printf("Warning: could not delete volume from vast.ai: %v\n", err)
				}
				s.volumes.Delete(vol.ID)
				break
			}
		}
	}

	return s.instances.Delete(inst.ID)
}

// Restart regenerates the startup script, kills the running server, and restarts it.
func (s *DeployService) Restart(id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	model, err := s.models.FindByName(inst.ModelName)
	if err != nil {
		return fmt.Errorf("model lookup: %w", err)
	}

	engine := s.engineFor(model)

	// Regenerate the startup script on the instance so fixes are picked up
	fmt.Println("Updating startup script...")
	onstart := engine.BuildOnstart(model, s.hfToken)
	// BuildOnstart returns: echo '...' > /tmp/script.sh && chmod +x ... && bash ...
	// Strip the final "&& bash ..." to only write the file without executing
	if idx := strings.LastIndex(onstart, " && bash "); idx > 0 {
		writeOnly := onstart[:idx]
		s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, writeOnly)
	}

	killCmd, startCmd := engine.RestartCommands(model)

	fmt.Printf("Restarting model server on instance %d (%s)...\n", inst.ID, inst.ModelName)

	fmt.Println("Stopping server...")
	s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, killCmd)

	fmt.Println("Starting server...")
	_, err = s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, startCmd)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	fmt.Println("Restart initiated. Use 'mycodeagent log -f' to monitor.")
	return nil
}

const defaultVolumeSizeGB = 50

// ensureVolume returns an existing or newly created volume for the offer's machine.
func (s *DeployService) ensureVolume(offer OfferResult, mountPath string) (int, string, error) {
	// Reuse existing volume on this machine (trust local DB — API doesn't show unattached volumes)
	vols, _ := s.volumes.FindAll()
	for _, vol := range vols {
		if vol.MachineID == offer.MachineID {
			fmt.Printf("Using existing volume %s on machine %d\n", vol.VolumeName, vol.MachineID)
			return int(vol.VastaiID), vol.MountPath, nil
		}
	}

	// Create new volume
	if offer.AvailVolAskID == nil {
		return 0, "", fmt.Errorf("machine %d has no volume offer", offer.MachineID)
	}

	fmt.Printf("Creating volume (%d GB) on machine %d...\n", defaultVolumeSizeGB, offer.MachineID)
	result, err := s.vastai.RentVolume(*offer.AvailVolAskID, defaultVolumeSizeGB)
	if err != nil {
		return 0, "", fmt.Errorf("create volume: %w", err)
	}

	// Parse volume ID from name (e.g. "V.34258398" → 34258398)
	var vastaiID int
	if strings.HasPrefix(result.VolumeName, "V.") {
		fmt.Sscanf(result.VolumeName[2:], "%d", &vastaiID)
	}
	if vastaiID == 0 {
		return 0, "", fmt.Errorf("could not parse volume ID from %s", result.VolumeName)
	}

	vol := &entity.Volume{
		VastaiID:   int64(vastaiID),
		VolumeName: result.VolumeName,
		SizeGB:     defaultVolumeSizeGB,
		MountPath:  mountPath,
		MachineID:  offer.MachineID,
	}
	if err := s.volumes.Save(vol); err != nil {
		return 0, "", fmt.Errorf("save volume: %w", err)
	}

	fmt.Printf("Volume %s created on machine %d\n", result.VolumeName, offer.MachineID)
	return vastaiID, mountPath, nil
}

