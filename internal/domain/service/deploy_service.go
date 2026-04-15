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
	// Offer / instance lifecycle (used by DeployService)
	SearchOffers(minGPURAM int, numGPUs int) ([]OfferResult, error)
	CreateInstance(offerID int, image string, envVars map[string]string, onstart string, volumeID int, mountPath string) (instanceID int, err error)
	WaitForInstance(ctx context.Context, instanceID int, volumeID int) (sshHost string, sshPort int, hourlyRate float64, err error)
	StopInstance(instanceID int) error
	DestroyInstance(instanceID int) error

	// Volume lifecycle (used by DeployService and VolumeService)
	SearchVolumeOffers(sizeGB int) ([]VolumeOfferResult, error)
	RentVolume(offerID int, sizeGB int) (*VolumeResult, error)
	WaitForVolumeReady(ctx context.Context, volumeID int) error
	ListVolumes() ([]VolumeResult, error)
	DeleteVolume(volumeID int) error

	// Read-side instance access (used by InstanceService for ps/tunnel/sync flows).
	// Returns service-package types so domain code never imports infrastructure/vastai.
	GetInstance(ctx context.Context, vastaiID int) (*RemoteInstance, error)
	ListRemoteInstances(ctx context.Context) ([]*RemoteInstance, error)
	GetInstanceLogs(ctx context.Context, vastaiID int, tail string) ([]byte, error)

	// Credential operations (used by InstanceService for the login flow).
	// These take an explicit apiKey because login verifies a key that is NOT
	// the one the adapter was constructed with — it's a fresh key the user
	// just typed in. Pass "" to use the adapter's configured key.
	VerifyAPIKey(ctx context.Context, apiKey string) error
	ListSSHKeys(ctx context.Context, apiKey string) ([]string, error)
	CreateSSHKey(ctx context.Context, apiKey string, pubKey string) error
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
	// BuildOnstart returns the onstart shell script for the instance.
	// numGPUs and contextLength come from the selected offer and the scaled context
	// computed by DeployService — they override anything in the model definition.
	BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string
	// BuildRawCommand returns a human-readable command for --create-instance-only output.
	BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string
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
	NumGPUs       int     // actual GPU count on this offer
	GPUMemory     float64 // per-GPU VRAM in MB as reported by vast.ai
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

// scaledContextLength grows model.ContextLength linearly with per-GPU VRAM
// headroom, capped at model.MaxContextLength. If MaxContextLength is unset or
// the offer doesn't report more VRAM than baseline, the baseline is returned.
// offerGPUMemoryMB is the per-GPU VRAM as reported by vast.ai (in MB).
func scaledContextLength(model *entity.Model, offerGPUMemoryMB float64) int {
	base := model.ContextLength
	if base <= 0 {
		return 0
	}
	maxCtx := model.MaxContextLength
	if maxCtx <= base {
		return base
	}
	if model.VRAM <= 0 || offerGPUMemoryMB <= 0 {
		return base
	}
	requiredGB := float64(model.VRAM)
	actualGB := offerGPUMemoryMB / 1024.0
	if actualGB <= requiredGB {
		return base
	}
	scaled := int(float64(base) * (actualGB / requiredGB))
	if scaled > maxCtx {
		scaled = maxCtx
	}
	if scaled < base {
		scaled = base
	}
	return scaled
}

// Deploy executes the full init flow: find offer → create instance → SSH → vLLM → tunnel.
// Unless noVolume is true, a volume is auto-created if none exists.
func (s *DeployService) Deploy(ctx context.Context, modelName string, noVolume bool) (*entity.Instance, error) {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return nil, err
	}

	timeout := model.StartupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fmt.Printf("Startup timeout: %s\n", timeout)

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
	fmt.Printf("Selected: %dx %s (%.0fGB each) at $%.3f/hr\n", offer.NumGPUs, offer.GPUName, offer.GPUMemory, offer.DPHTotal)

	engine := s.engineFor(model)
	contextLength := scaledContextLength(model, offer.GPUMemory)
	if contextLength > 0 && contextLength != model.ContextLength {
		fmt.Printf("Context length scaled: %d → %d (offer has %.0fGB per GPU vs baseline %dGB)\n",
			model.ContextLength, contextLength, offer.GPUMemory/1024.0, model.VRAM)
	}
	onstart := engine.BuildOnstart(model, offer.NumGPUs, contextLength, s.hfToken)

	envVars := map[string]string{}
	if s.hfToken != "" {
		envVars["HF_TOKEN"] = s.hfToken
		envVars["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}

	instanceID, volumeID, _, err := s.createInstanceWithVolumeRetry(ctx, offer, engine, envVars, onstart, noVolume)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID, volumeID)
	if err != nil {
		return nil, fmt.Errorf("wait for instance: %w", err)
	}
	fmt.Printf("Instance running: SSH at %s:%d\n", sshHost, sshPort)

	fmt.Println("Waiting for SSH...")
	if err := s.ssh.WaitForSSH(ctx, sshHost, sshPort); err != nil {
		return nil, fmt.Errorf("wait for SSH: %w", err)
	}

	localPort, err := s.ssh.FindFreePort(s.basePort)
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}

	fmt.Printf("Starting SSH tunnel on port %d...\n", localPort)
	tunnelPID, err := s.ssh.StartTunnel(localPort, sshHost, sshPort)
	if err != nil {
		return nil, fmt.Errorf("start tunnel: %w", err)
	}

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
		NumGPUs:    offer.NumGPUs,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	fmt.Println("Waiting for model server to become healthy (model downloading, this may take a while)...")
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
func (s *DeployService) DeployCreateOnly(ctx context.Context, modelName string, noVolume bool) (*CreateOnlyResult, error) {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return nil, err
	}

	timeout := model.StartupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
	fmt.Printf("Selected: %dx %s (%.0fGB each) at $%.3f/hr\n", offer.NumGPUs, offer.GPUName, offer.GPUMemory, offer.DPHTotal)

	engine := s.engineFor(model)
	contextLength := scaledContextLength(model, offer.GPUMemory)
	onstart := engine.BuildOnstart(model, offer.NumGPUs, contextLength, s.hfToken)

	envVars := map[string]string{}
	if s.hfToken != "" {
		envVars["HF_TOKEN"] = s.hfToken
		envVars["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}

	instanceID, volumeID, _, err := s.createInstanceWithVolumeRetry(ctx, offer, engine, envVars, onstart, noVolume)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID, volumeID)
	if err != nil {
		return nil, fmt.Errorf("wait for instance: %w", err)
	}

	inst := &entity.Instance{
		VastaiID:   int64(instanceID),
		ModelName:  model.Name,
		Status:     entity.StatusRunning,
		SSHHost:    sshHost,
		SSHPort:    sshPort,
		HourlyRate: hourlyRate,
		VolumeID:   int64(volumeID),
		NumGPUs:    offer.NumGPUs,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	return &CreateOnlyResult{Instance: inst, ServeCommand: engine.BuildRawCommand(model, offer.NumGPUs, contextLength)}, nil
}

// Stop stops a single instance by local DB ID.
func (s *DeployService) Stop(ctx context.Context, id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	if err := s.ssh.StopTunnel(inst.TunnelPID); err != nil {
		fmt.Printf("Warning: failed to stop tunnel: %v\n", err)
	}

	if err := s.vastai.StopInstance(int(inst.VastaiID)); err != nil {
		return fmt.Errorf("stop vast.ai instance: %w", err)
	}

	inst.Status = entity.StatusStopped
	inst.TunnelPID = 0
	return s.instances.Update(inst)
}

// Destroy destroys a single instance and its volumes permanently.
func (s *DeployService) Destroy(ctx context.Context, id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	if err := s.ssh.StopTunnel(inst.TunnelPID); err != nil {
		fmt.Printf("Warning: failed to stop tunnel: %v\n", err)
	}

	if err := s.vastai.DestroyInstance(int(inst.VastaiID)); err != nil {
		return fmt.Errorf("destroy vast.ai instance: %w", err)
	}

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
func (s *DeployService) Restart(ctx context.Context, id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	model, err := s.models.FindByName(inst.ModelName)
	if err != nil {
		return fmt.Errorf("model lookup: %w", err)
	}

	engine := s.engineFor(model)

	// Regenerate the startup script on the instance so fixes are picked up.
	// Use the GPU count actually allocated at deploy time (persisted on the instance row),
	// falling back to model.NumGPUs for legacy rows that predate the column.
	// Restart happens after the offer is gone, so use the model's baseline context length.
	numGPUs := inst.NumGPUs
	if numGPUs <= 0 {
		numGPUs = model.NumGPUs
	}
	fmt.Println("Updating startup script...")
	onstart := engine.BuildOnstart(model, numGPUs, model.ContextLength, s.hfToken)
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

// createInstanceWithVolumeRetry resolves the volume for the offer's machine and
// creates the instance. If vast.ai rejects the call with a stale-volume 404
// (e.g. the volume was auto-removed by vast.ai or left over from a failed run),
// it deletes the local DB record and retries once with a freshly rented volume.
func (s *DeployService) createInstanceWithVolumeRetry(
	ctx context.Context, offer OfferResult, engine EngineProvider, envVars map[string]string,
	onstart string, noVolume bool,
) (instanceID int, volumeID int, mountPath string, err error) {
	var reused bool
	if !noVolume {
		volumeID, mountPath, reused, err = s.ensureVolume(ctx, offer, engine.VolumeMountPath())
		if err != nil {
			return 0, 0, "", err
		}
	}

	fmt.Println("Creating instance...")
	instanceID, err = s.vastai.CreateInstance(offer.ID, engine.DockerImage(), envVars, onstart, volumeID, mountPath)
	if err == nil {
		return instanceID, volumeID, mountPath, nil
	}

	// Stale-volume retry: only meaningful when the volume came from the local DB.
	// A freshly rented volume has already been verified ready, so a 404 there means
	// something else is wrong and retrying would just orphan more volumes.
	if !noVolume && reused && volumeID > 0 && isStaleVolumeError(err) {
		fmt.Printf("Volume V.%d is stale on vast.ai — removing local record and renting a fresh one\n", volumeID)
		if delErr := s.deleteLocalVolumeByVastaiID(int64(volumeID)); delErr != nil {
			fmt.Printf("Warning: failed to remove stale volume record: %v\n", delErr)
		}
		volumeID, mountPath, _, err = s.ensureVolume(ctx, offer, engine.VolumeMountPath())
		if err != nil {
			return 0, 0, "", err
		}
		fmt.Println("Creating instance (retry with fresh volume)...")
		instanceID, err = s.vastai.CreateInstance(offer.ID, engine.DockerImage(), envVars, onstart, volumeID, mountPath)
		if err != nil {
			return 0, 0, "", fmt.Errorf("create instance after volume refresh: %w", err)
		}
		return instanceID, volumeID, mountPath, nil
	}

	return 0, 0, "", fmt.Errorf("create instance: %w", err)
}

// isStaleVolumeError detects vast.ai's 404 response when a volume_id no longer exists.
func isStaleVolumeError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "404") &&
		strings.Contains(msg, "Volume") &&
		strings.Contains(msg, "does not exist")
}

// deleteLocalVolumeByVastaiID removes a volume row from SQLite by its vast.ai ID.
func (s *DeployService) deleteLocalVolumeByVastaiID(vastaiID int64) error {
	vols, err := s.volumes.FindAll()
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.VastaiID == vastaiID {
			return s.volumes.Delete(v.ID)
		}
	}
	return nil
}

// ensureVolume returns an existing or newly created volume for the offer's machine.
// When a new volume is rented, it waits for vast.ai to transition the volume out of
// "initialized" state before returning, otherwise CreateInstance will reject it.
// The reused return value is true when an existing local DB row was reused (so the
// caller can decide whether a subsequent 404 should trigger a stale-row cleanup).
func (s *DeployService) ensureVolume(ctx context.Context, offer OfferResult, mountPath string) (volumeID int, mount string, reused bool, err error) {
	// Reuse existing volume on this machine (trust local DB — API doesn't show unattached volumes)
	vols, _ := s.volumes.FindAll()
	for _, vol := range vols {
		if vol.MachineID == offer.MachineID {
			fmt.Printf("Using existing volume %s on machine %d\n", vol.VolumeName, vol.MachineID)
			return int(vol.VastaiID), vol.MountPath, true, nil
		}
	}

	// Create new volume
	if offer.AvailVolAskID == nil {
		return 0, "", false, fmt.Errorf("machine %d has no volume offer", offer.MachineID)
	}

	fmt.Printf("Creating volume (%d GB) on machine %d...\n", defaultVolumeSizeGB, offer.MachineID)
	result, err := s.vastai.RentVolume(*offer.AvailVolAskID, defaultVolumeSizeGB)
	if err != nil {
		return 0, "", false, fmt.Errorf("create volume: %w", err)
	}

	// Parse volume ID from name (e.g. "V.34258398" → 34258398)
	var vastaiID int
	if strings.HasPrefix(result.VolumeName, "V.") {
		fmt.Sscanf(result.VolumeName[2:], "%d", &vastaiID)
	}
	if vastaiID == 0 {
		return 0, "", false, fmt.Errorf("could not parse volume ID from %s", result.VolumeName)
	}

	vol := &entity.Volume{
		VastaiID:   int64(vastaiID),
		VolumeName: result.VolumeName,
		SizeGB:     defaultVolumeSizeGB,
		MountPath:  mountPath,
		MachineID:  offer.MachineID,
	}
	if err := s.volumes.Save(vol); err != nil {
		return 0, "", false, fmt.Errorf("save volume: %w", err)
	}

	fmt.Printf("Volume %s created on machine %d\n", result.VolumeName, offer.MachineID)
	fmt.Println("Waiting for volume to leave 'initialized' state...")
	if err := s.vastai.WaitForVolumeReady(ctx, vastaiID); err != nil {
		return 0, "", false, fmt.Errorf("wait for volume ready: %w", err)
	}
	return vastaiID, mountPath, false, nil
}
