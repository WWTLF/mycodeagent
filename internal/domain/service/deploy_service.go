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
	SearchOffers(minGPURAM int, numGPUs int, minDiskGB int) ([]OfferResult, error)
	CreateInstance(offerID int, image string, envVars map[string]string, onstart string, diskGB int) (instanceID int, err error)
	WaitForInstance(ctx context.Context, instanceID int) (sshHost string, sshPort int, hourlyRate float64, err error)
	StopInstance(instanceID int) error
	DestroyInstance(instanceID int) error

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

// EngineProvider abstracts engine-specific deployment details. There is one
// implementation (vLLM); the interface is kept so the domain layer depends on
// an abstraction rather than the concrete infrastructure type.
type EngineProvider interface {
	// DockerImage returns the Docker image to use on vast.ai
	DockerImage() string
	// BuildOnstart returns the onstart shell script for the instance.
	// numGPUs and contextLength come from the selected offer and the scaled context
	// computed by DeployService — they override anything in the model definition.
	BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string
	// BuildRawCommand returns a human-readable command for --create-instance-only output.
	BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string
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
	ID        int
	GPUName   string
	NumGPUs   int     // actual GPU count on this offer
	GPUMemory float64 // per-GPU VRAM in MB as reported by vast.ai
	DPHTotal  float64
	MachineID int
}

// defaultDiskGB is the container disk requested when a model doesn't specify one.
const defaultDiskGB = 40

// diskHeadroomGB is added to a model's disk request when filtering host offers:
// the vllm-openai image is ~15 GB and we want scratch on top of the model download.
const diskHeadroomGB = 25

type DeployService struct {
	models    repository.ModelRepository
	instances repository.InstanceRepository
	badHosts  repository.BadHostRepository
	vastai    VastaiProvider
	ssh       SSHTunnelProvider
	engine    EngineProvider
	basePort  int
	hfToken   string
}

func NewDeployService(
	models repository.ModelRepository,
	instances repository.InstanceRepository,
	badHosts repository.BadHostRepository,
	vastai VastaiProvider,
	ssh SSHTunnelProvider,
	engine EngineProvider,
	basePort int,
	hfToken string,
) *DeployService {
	return &DeployService{
		models:    models,
		instances: instances,
		badHosts:  badHosts,
		vastai:    vastai,
		ssh:       ssh,
		engine:    engine,
		basePort:  basePort,
		hfToken:   hfToken,
	}
}

// diskFor returns the container disk size (GB) to request for a model.
func diskFor(model *entity.Model) int {
	if model.DiskGB > 0 {
		return model.DiskGB
	}
	return defaultDiskGB
}

// markHostBad records a machine_id as bad so future SearchOffers skip it.
// Tolerant of a nil repo or a DB error — the deploy already failed, we just
// want to record what we can without masking the original error.
//
// IMPORTANT: only call this for *host-side* failures (instance never reached
// running, SSH never came up). Model-side failures (server crashed, health
// timed out) are not the host's fault, and blaming the host would slowly
// blacklist every good machine until "no offers found".
func (s *DeployService) markHostBad(machineID int, reason string) {
	if s.badHosts == nil || machineID == 0 {
		return
	}
	if err := s.badHosts.Add(machineID, reason); err != nil {
		fmt.Printf("warn: failed to record bad host %d: %v\n", machineID, err)
		return
	}
	fmt.Printf("Recorded machine %d as bad host (%s)\n", machineID, reason)
}

// filterBadHosts drops offers whose MachineID is marked bad. Returns the full
// list unchanged if the bad-host lookup fails — a flaky local DB must not block
// the deploy. Silently returns all offers when the bad set is empty.
func (s *DeployService) filterBadHosts(offers []OfferResult) []OfferResult {
	if s.badHosts == nil || len(offers) == 0 {
		return offers
	}
	bad, err := s.badHosts.List()
	if err != nil || len(bad) == 0 {
		return offers
	}
	skip := make(map[int]bool, len(bad))
	for _, h := range bad {
		skip[h.MachineID] = true
	}
	out := make([]OfferResult, 0, len(offers))
	for _, o := range offers {
		if skip[o.MachineID] {
			continue
		}
		out = append(out, o)
	}
	if dropped := len(offers) - len(out); dropped > 0 {
		fmt.Printf("Skipping %d offer(s) on known-bad hosts\n", dropped)
	}
	return out
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
// Instances are disposable: if any step after creation fails, the vast.ai instance
// is destroyed and the tunnel killed so a failed deploy never leaves a paid GPU running.
func (s *DeployService) Deploy(ctx context.Context, modelName string) (inst *entity.Instance, err error) {
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
	diskGB := diskFor(model)
	minHostDisk := diskGB + diskHeadroomGB
	fmt.Printf("Searching for %dx GPU with >= %dGB VRAM (host disk >= %dGB)...\n", numGPUs, model.VRAM, minHostDisk)
	offers, err := s.vastai.SearchOffers(model.VRAM, numGPUs, minHostDisk)
	if err != nil {
		return nil, fmt.Errorf("search offers: %w", err)
	}
	offers = s.filterBadHosts(offers)
	if len(offers) == 0 {
		return nil, fmt.Errorf("no GPU offers found with %dx >= %dGB VRAM (after filtering bad hosts)", numGPUs, model.VRAM)
	}
	offer := offers[0] // cheapest (already sorted)
	fmt.Printf("Selected: %dx %s (%.0fGB each) at $%.3f/hr\n", offer.NumGPUs, offer.GPUName, offer.GPUMemory, offer.DPHTotal)

	contextLength := scaledContextLength(model, offer.GPUMemory)
	if contextLength > 0 && contextLength != model.ContextLength {
		fmt.Printf("Context length scaled: %d → %d (offer has %.0fGB per GPU vs baseline %dGB)\n",
			model.ContextLength, contextLength, offer.GPUMemory/1024.0, model.VRAM)
	}
	onstart := s.engine.BuildOnstart(model, offer.NumGPUs, contextLength, s.hfToken)

	envVars := s.engineEnv()

	fmt.Printf("Creating instance (disk %dGB)...\n", diskGB)
	instanceID, err := s.vastai.CreateInstance(offer.ID, s.engine.DockerImage(), envVars, onstart, diskGB)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	// From here on the vast.ai instance is billing. Any failure must tear it
	// down (and the tunnel + DB row) so a failed startup leaves nothing running.
	deployed := false
	tunnelPID := 0
	defer func() {
		if deployed {
			return
		}
		if tunnelPID > 0 {
			_ = s.ssh.StopTunnel(tunnelPID)
		}
		if destroyErr := s.vastai.DestroyInstance(instanceID); destroyErr != nil {
			fmt.Printf("Warning: failed to destroy instance %d during cleanup: %v\n", instanceID, destroyErr)
		}
		if inst != nil && inst.ID > 0 {
			_ = s.instances.Delete(inst.ID)
		}
		fmt.Printf("Destroyed instance %d after failed deploy (no further charges).\n", instanceID)
	}()

	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID)
	if err != nil {
		// Instance never reached running → host-side problem, blacklist it.
		s.markHostBad(offer.MachineID, fmt.Sprintf("wait for instance: %v", err))
		return nil, fmt.Errorf("wait for instance: %w", err)
	}
	fmt.Printf("Instance running: SSH at %s:%d\n", sshHost, sshPort)

	fmt.Println("Waiting for SSH...")
	if err := s.ssh.WaitForSSH(ctx, sshHost, sshPort); err != nil {
		// SSH never came up → host-side problem, blacklist it.
		s.markHostBad(offer.MachineID, fmt.Sprintf("wait for SSH: %v", err))
		return nil, fmt.Errorf("wait for SSH: %w", err)
	}

	localPort, err := s.ssh.FindFreePort(s.basePort)
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}

	fmt.Printf("Starting SSH tunnel on port %d...\n", localPort)
	tunnelPID, err = s.ssh.StartTunnel(localPort, sshHost, sshPort)
	if err != nil {
		return nil, fmt.Errorf("start tunnel: %w", err)
	}

	inst = &entity.Instance{
		VastaiID:   int64(instanceID),
		ModelName:  model.Name,
		Status:     entity.StatusRunning,
		LocalPort:  localPort,
		SSHHost:    sshHost,
		SSHPort:    sshPort,
		TunnelPID:  tunnelPID,
		HourlyRate: hourlyRate,
		NumGPUs:    offer.NumGPUs,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	fmt.Println("Waiting for model server to become healthy (model downloading, this may take a while)...")
	healthCh := make(chan error, 1)
	failCh := make(chan error, 1)
	go func() {
		healthCh <- s.ssh.WaitForVLLMHealth(ctx, localPort)
	}()
	// Liveness watcher: if the vllm process dies during startup, abort early
	// with the tail of its log instead of polling a dead port until timeout.
	go s.watchServerProcess(ctx, sshHost, sshPort, failCh)

	select {
	case healthErr := <-healthCh:
		if healthErr != nil {
			// Model-side failure — do NOT blacklist the host.
			return nil, fmt.Errorf("vLLM health check: %w", healthErr)
		}
	case crashErr := <-failCh:
		return nil, fmt.Errorf("model server crashed during startup: %w", crashErr)
	case <-ctx.Done():
		return nil, fmt.Errorf("startup timed out after %s", timeout)
	}

	deployed = true
	fmt.Printf("\nAPI available at: http://localhost:%d/v1\n", localPort)
	return inst, nil
}

// watchServerProcess polls the remote host over SSH and reports (via failCh)
// when the vllm process is no longer running, so Deploy can fail fast on a
// crash instead of waiting out the full startup timeout. It requires two
// consecutive "dead" reads to avoid a false positive during a brief re-exec.
func (s *DeployService) watchServerProcess(ctx context.Context, sshHost string, sshPort int, failCh chan<- error) {
	// Grace period: let the onstart script write itself and launch vllm.
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	deadCount := 0
	for {
		out, err := s.ssh.RunRemoteCommand(sshHost, sshPort,
			"pgrep -f 'vllm serve' >/dev/null 2>&1 && echo ALIVE || echo DEAD")
		switch {
		case err != nil:
			deadCount = 0 // SSH hiccup, not a crash signal
		case strings.Contains(string(out), "DEAD"):
			deadCount++
			if deadCount >= 2 {
				logTail, _ := s.ssh.RunRemoteCommand(sshHost, sshPort, "tail -n 25 /tmp/vllm.log 2>/dev/null")
				failCh <- fmt.Errorf("vllm process is no longer running; last log lines:\n%s",
					strings.TrimSpace(string(logTail)))
				return
			}
		default:
			deadCount = 0
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// engineEnv builds the per-instance environment (HF token for gated repos /
// higher download rate limits).
func (s *DeployService) engineEnv() map[string]string {
	env := map[string]string{}
	if s.hfToken != "" {
		env["HF_TOKEN"] = s.hfToken
		env["HUGGING_FACE_HUB_TOKEN"] = s.hfToken
	}
	return env
}

type CreateOnlyResult struct {
	Instance     *entity.Instance
	ServeCommand string
}

// DeployCreateOnly creates the instance and waits for it to be running, but does not
// set up the SSH tunnel or wait for vLLM health. The instance is intentionally left
// running so the user can attach manually — no failure cleanup here.
func (s *DeployService) DeployCreateOnly(ctx context.Context, modelName string) (*CreateOnlyResult, error) {
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
	diskGB := diskFor(model)
	minHostDisk := diskGB + diskHeadroomGB
	fmt.Printf("Searching for %dx GPU with >= %dGB VRAM (host disk >= %dGB)...\n", numGPUs, model.VRAM, minHostDisk)
	offers, err := s.vastai.SearchOffers(model.VRAM, numGPUs, minHostDisk)
	if err != nil {
		return nil, fmt.Errorf("search offers: %w", err)
	}
	offers = s.filterBadHosts(offers)
	if len(offers) == 0 {
		return nil, fmt.Errorf("no GPU offers found with %dx >= %dGB VRAM (after filtering bad hosts)", numGPUs, model.VRAM)
	}
	offer := offers[0]
	fmt.Printf("Selected: %dx %s (%.0fGB each) at $%.3f/hr\n", offer.NumGPUs, offer.GPUName, offer.GPUMemory, offer.DPHTotal)

	contextLength := scaledContextLength(model, offer.GPUMemory)
	onstart := s.engine.BuildOnstart(model, offer.NumGPUs, contextLength, s.hfToken)

	fmt.Printf("Creating instance (disk %dGB)...\n", diskGB)
	instanceID, err := s.vastai.CreateInstance(offer.ID, s.engine.DockerImage(), s.engineEnv(), onstart, diskGB)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	fmt.Printf("Instance created: %d\n", instanceID)

	fmt.Println("Waiting for instance to start...")
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID)
	if err != nil {
		s.markHostBad(offer.MachineID, fmt.Sprintf("wait for instance: %v", err))
		return nil, fmt.Errorf("wait for instance: %w", err)
	}

	inst := &entity.Instance{
		VastaiID:   int64(instanceID),
		ModelName:  model.Name,
		Status:     entity.StatusRunning,
		SSHHost:    sshHost,
		SSHPort:    sshPort,
		HourlyRate: hourlyRate,
		NumGPUs:    offer.NumGPUs,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	return &CreateOnlyResult{Instance: inst, ServeCommand: s.engine.BuildRawCommand(model, offer.NumGPUs, contextLength)}, nil
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

// Destroy destroys a single instance permanently.
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

	// Regenerate the startup script on the instance so fixes are picked up.
	// Use the GPU count actually allocated at deploy time (persisted on the instance row),
	// falling back to model.NumGPUs for legacy rows that predate the column.
	// Restart happens after the offer is gone, so use the model's baseline context length.
	numGPUs := inst.NumGPUs
	if numGPUs <= 0 {
		numGPUs = model.NumGPUs
	}
	fmt.Println("Updating startup script...")
	onstart := s.engine.BuildOnstart(model, numGPUs, model.ContextLength, s.hfToken)
	// BuildOnstart returns: echo '...' > /tmp/script.sh && chmod +x ... && bash ...
	// Strip the final "&& bash ..." to only write the file without executing
	if idx := strings.LastIndex(onstart, " && bash "); idx > 0 {
		writeOnly := onstart[:idx]
		s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, writeOnly)
	}

	killCmd, startCmd := s.engine.RestartCommands(model)

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
