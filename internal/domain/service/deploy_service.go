package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	StartInstance(instanceID int) error
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
// implementation (llama.cpp); the interface is kept so the domain layer depends
// on an abstraction rather than the concrete infrastructure type.
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
	// LivenessCommand returns a remote shell command that prints ALIVE or DEAD
	// depending on whether the model server process is still running. Keeping it
	// here means the domain never hardcodes a process name or a probing tool.
	LivenessCommand() string
	// DownloadedBytesCommand returns a remote shell command that prints the
	// number of bytes fetched into the model cache so far, or 0. The cache
	// location is engine-specific, which is why this lives behind the interface.
	DownloadedBytesCommand() string
	// LogPath returns the remote path the server's stdout/stderr is teed to.
	LogPath() string
}

// SSHTunnelProvider abstracts SSH tunnel operations for the domain layer.
type SSHTunnelProvider interface {
	StartTunnel(localPort int, sshHost string, sshPort int) (pid int, err error)
	StopTunnel(pid int) error
	WaitForSSH(ctx context.Context, host string, port int) error
	RunRemoteCommand(sshHost string, sshPort int, command string) ([]byte, error)
	FindFreePort(basePort int) (int, error)
	WaitForServerHealth(ctx context.Context, localPort int) error
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

// defaultStartupTimeout applies to a model with no StartupTimeout of its own.
// Sized like the catalog entries (see model_repo_static.go): the provisioning
// phase alone has been measured at 9.3 minutes on a slow host, so anything under
// ~15 minutes can fail before the model server is even reached.
const defaultStartupTimeout = 20 * time.Minute

// slowProvisionThreshold is how long the *host* may take to bring an instance to
// "running" before it counts as evidence against that host.
//
// Provisioning is the host pulling and unpacking the image. It is the one phase
// no model, quant or flag can influence, which is what makes it usable as
// evidence even when the deploy failed later for a reason that looks model-side.
// Healthy hosts finish in seconds to about a minute; the machine that motivated
// this took 9.3 minutes, then ran llama-server so slowly it never started the
// download. The budget in model_repo_static.go tolerates up to 10 minutes here,
// so anything past half of that is already an outlier.
//
// A variable, like the watcher cadence below, so a test can drive the positive
// path without sleeping for minutes.
var slowProvisionThreshold = 5 * time.Minute

// blameHostForTimeout decides whether a startup timeout is the host's fault.
//
// Kept separate from markHostBad, and pure, because the judgement is the risky
// part: blaming a host wrongly removes a good machine from the offer pool, and
// doing that systematically ends in "no offers found". A timeout alone is not
// enough — a legitimately slow download looks identical — so the deciding
// evidence is that the host had already burned an outlier amount of the budget
// before the model was even reached.
func blameHostForTimeout(provisioning time.Duration) bool {
	return provisioning > slowProvisionThreshold
}

// diskHeadroomGB is added to a model's disk request when filtering host offers:
// the llama.cpp server-cuda image is ~2.6 GB compressed / ~6 GB unpacked, and the
// host needs room for both plus scratch on top of the GGUF download.
const diskHeadroomGB = 12

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

// Liveness watcher cadence. Variables rather than constants purely so tests can
// shrink them: the crash path is otherwise unreachable in under two minutes, and
// it guards the rule that a model-side crash must never blacklist a host.
var (
	// livenessGracePeriod gives the onstart script time to write itself and
	// exec the server before a missing process counts as a crash.
	livenessGracePeriod = 90 * time.Second
	livenessInterval    = 20 * time.Second
)

// markHostBadUnlessCancelled records a host-side failure, except when the cause
// was the user interrupting the deploy.
//
// Ctrl-C surfaces as context.Canceled from whichever wait was in flight, and the
// wait phases cannot tell it apart from a real failure without this check. The
// consequence was silent and cumulative: every aborted deploy blacklisted a
// perfectly good machine, so a session spent aborting slow deploys would drain
// the offer pool one host at a time — the exact failure the crash-path rule was
// written to avoid, arriving through a different door.
func (s *DeployService) markHostBadUnlessCancelled(machineID int, phase string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.markHostBad(machineID, fmt.Sprintf("%s: %v", phase, err))
}

// blameHostIfSlowToProvision records the host only when a startup timeout is
// attributable to it. Called from the timeout paths, never from the crash path.
func (s *DeployService) blameHostIfSlowToProvision(machineID int, provisioning time.Duration) {
	if !blameHostForTimeout(provisioning) {
		return
	}
	s.markHostBad(machineID, fmt.Sprintf(
		"startup timed out; host spent %s just provisioning", provisioning.Round(time.Second)))
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

// Deploy executes the full init flow: find offer → create instance → SSH → tunnel → health.
// Instances are disposable: if any step after creation fails, the vast.ai instance
// is destroyed and the tunnel killed so a failed deploy never leaves a paid GPU running.
func (s *DeployService) Deploy(ctx context.Context, modelName string) (*entity.Instance, error) {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return nil, err
	}

	timeout := model.StartupTimeout
	if timeout <= 0 {
		timeout = defaultStartupTimeout
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
	//
	// tunnelPID and savedID are plain locals rather than the named results on
	// purpose: every failure path below does `return nil, err`, which would
	// assign nil to a named `inst` result *before* this deferred function runs,
	// so a cleanup keyed on `inst` would silently skip the row delete.
	deployed := false
	tunnelPID := 0
	var savedID int64
	defer func() {
		if deployed {
			return
		}
		// Print before the API calls: on Ctrl-C this is the user's signal that
		// teardown is in progress and a second interrupt would abort it.
		fmt.Printf("\nCleaning up: destroying instance %d so it stops billing...\n", instanceID)
		if tunnelPID > 0 {
			_ = s.ssh.StopTunnel(tunnelPID)
		}
		if destroyErr := s.vastai.DestroyInstance(instanceID); destroyErr != nil {
			fmt.Printf("Warning: failed to destroy instance %d during cleanup: %v\n", instanceID, destroyErr)
			fmt.Printf("IMPORTANT: destroy it manually — `mycodeagent ps` then `mycodeagent kill <id>`.\n")
		} else {
			fmt.Printf("Destroyed instance %d after failed deploy (no further charges).\n", instanceID)
		}
		if savedID > 0 {
			_ = s.instances.Delete(savedID)
		}
	}()

	fmt.Println("Waiting for instance to start...")
	provisionStart := time.Now()
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, instanceID)
	if err != nil {
		// Instance never reached running → host-side problem, blacklist it —
		// unless the user interrupted, which says nothing about the machine.
		s.markHostBadUnlessCancelled(offer.MachineID, "wait for instance", err)
		return nil, fmt.Errorf("wait for instance: %w", err)
	}
	// How long the host took to pull and unpack the image. Retained because a
	// timeout later on may still be this host's fault — see blameHostForTimeout.
	provisioning := time.Since(provisionStart)
	fmt.Printf("Instance running: SSH at %s:%d (provisioned in %s)\n",
		sshHost, sshPort, provisioning.Round(time.Second))

	fmt.Println("Waiting for SSH...")
	if err := s.ssh.WaitForSSH(ctx, sshHost, sshPort); err != nil {
		// SSH never came up → host-side problem, blacklist it (same caveat).
		s.markHostBadUnlessCancelled(offer.MachineID, "wait for SSH", err)
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

	inst := &entity.Instance{
		VastaiID:      int64(instanceID),
		ModelName:     model.Name,
		Status:        entity.StatusRunning,
		LocalPort:     localPort,
		SSHHost:       sshHost,
		SSHPort:       sshPort,
		TunnelPID:     tunnelPID,
		HourlyRate:    hourlyRate,
		NumGPUs:       offer.NumGPUs,
		ContextLength: contextLength,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}
	savedID = inst.ID

	fmt.Println("Waiting for model server to become healthy (model downloading, this may take a while)...")
	healthCh := make(chan error, 1)
	failCh := make(chan error, 1)
	go func() {
		healthCh <- s.ssh.WaitForServerHealth(ctx, localPort)
	}()
	// Liveness watcher: if the server process dies during startup, abort early
	// with the tail of its log instead of polling a dead port until timeout.
	go s.watchServerProcess(ctx, model, sshHost, sshPort, failCh)

	select {
	case healthErr := <-healthCh:
		if healthErr != nil {
			// The health poll only ever fails by running out of time, so this is
			// the same situation as ctx.Done below.
			s.blameHostIfSlowToProvision(offer.MachineID, provisioning)
			return nil, fmt.Errorf("model server health check: %w", healthErr)
		}
	case crashErr := <-failCh:
		// The server process died. That is the model's doing — a bad quant, a
		// flag the build rejects, not enough VRAM — and blaming the host here
		// would slowly blacklist every good machine.
		return nil, fmt.Errorf("model server crashed during startup: %w", crashErr)
	case <-ctx.Done():
		// Distinguish "we ran out of time" from "the user pressed Ctrl-C" —
		// both land here, but only one of them is a problem to investigate.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("startup interrupted")
		}
		s.blameHostIfSlowToProvision(offer.MachineID, provisioning)
		return nil, fmt.Errorf("startup timed out after %s", timeout)
	}

	deployed = true
	fmt.Printf("\nAPI available at: http://localhost:%d/v1\n", localPort)
	return inst, nil
}

// watchServerProcess polls the remote host over SSH and reports (via failCh)
// when the model server process is no longer running, so Deploy can fail fast on
// a crash instead of waiting out the full startup timeout. It requires two
// consecutive "dead" reads to avoid a false positive during a brief re-exec.
// The probe command and log path come from the engine, not from this layer.
func (s *DeployService) watchServerProcess(ctx context.Context, model *entity.Model, sshHost string, sshPort int, failCh chan<- error) {
	// Grace period: let the onstart script write itself and launch the server.
	select {
	case <-ctx.Done():
		return
	case <-time.After(livenessGracePeriod):
	}

	ticker := time.NewTicker(livenessInterval)
	defer ticker.Stop()

	liveness := s.engine.LivenessCommand()
	tailCmd := fmt.Sprintf("tail -n 25 %s 2>/dev/null", s.engine.LogPath())
	progress := newDownloadReporter(model)

	deadCount := 0
	for {
		// Same tick, second call: the model server prints nothing at all while
		// fetching the GGUF, so without this the whole download — minutes, on a
		// 25 GB model — is indistinguishable from a hang.
		if bytesOut, err := s.ssh.RunRemoteCommand(sshHost, sshPort, s.engine.DownloadedBytesCommand()); err == nil {
			progress.report(bytesOut)
		}

		out, err := s.ssh.RunRemoteCommand(sshHost, sshPort, liveness)
		switch {
		case err != nil:
			deadCount = 0 // SSH hiccup, not a crash signal
		case strings.Contains(string(out), "DEAD"):
			deadCount++
			if deadCount >= 2 {
				logTail, _ := s.ssh.RunRemoteCommand(sshHost, sshPort, tailCmd)
				failCh <- fmt.Errorf("model server process is no longer running; last log lines:\n%s",
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

// downloadReporter turns successive byte counts from the instance into a line
// the user can read, and stays quiet once the download is done.
//
// It exists because llama.cpp emits nothing while fetching a GGUF: the log sits
// unchanged and the tunnel logs "connect_to localhost port 8000: failed" on
// repeat, which looks exactly like a hang. Several deploys were aborted on that
// suspicion before anyone confirmed the bytes were still arriving.
type downloadReporter struct {
	totalBytes float64 // 0 when the model declares no size — then report bytes only
	last       int64
	lastAt     time.Time
	done       bool
}

func newDownloadReporter(model *entity.Model) *downloadReporter {
	return &downloadReporter{totalBytes: model.DownloadGB * bytesPerGB}
}

// Decimal units throughout, because that is what the sizes are measured in:
// HuggingFace reports file sizes in decimal GB and the catalog figures were
// copied from there. Verified against a finished download — `du -sb` returned
// 25 636 485 460 bytes for the entry the catalog calls 25.6 GB, which is exactly
// 25.6e9 and not 25.6 GiB. Dividing by 1024³ made a complete download read 93%.
const (
	bytesPerGB = 1e9
	bytesPerMB = 1e6
)

// report parses one sample and prints progress when it has moved.
func (r *downloadReporter) report(out []byte) {
	if r.done {
		return
	}
	got, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || got <= 0 {
		return
	}
	now := time.Now()
	if r.last == 0 {
		r.last, r.lastAt = got, now
		return // first sample: no rate to report yet
	}
	if got == r.last {
		return // nothing new since the last tick; stay quiet
	}

	var rate string
	if elapsed := now.Sub(r.lastAt).Seconds(); elapsed > 0 {
		rate = fmt.Sprintf(" at %.0f MB/s", float64(got-r.last)/elapsed/bytesPerMB)
	}
	if r.totalBytes > 0 {
		pct := float64(got) / r.totalBytes * 100
		if pct > 100 {
			// The cache also holds the odd config/tokenizer file, so the ratio can
			// tip just past the GGUF's own size. Don't print 103%.
			pct = 100
		}
		fmt.Printf("  downloading model: %.1f / %.1f GB (%.0f%%)%s\n",
			float64(got)/bytesPerGB, r.totalBytes/bytesPerGB, pct, rate)
	} else {
		fmt.Printf("  downloading model: %.1f GB%s\n", float64(got)/bytesPerGB, rate)
	}
	r.last, r.lastAt = got, now
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
// set up the SSH tunnel or wait for server health. The instance is intentionally left
// running so the user can attach manually — no failure cleanup here.
func (s *DeployService) DeployCreateOnly(ctx context.Context, modelName string) (*CreateOnlyResult, error) {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return nil, err
	}

	timeout := model.StartupTimeout
	if timeout <= 0 {
		timeout = defaultStartupTimeout
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
		s.markHostBadUnlessCancelled(offer.MachineID, "wait for instance", err)
		return nil, fmt.Errorf("wait for instance: %w", err)
	}

	inst := &entity.Instance{
		VastaiID:      int64(instanceID),
		ModelName:     model.Name,
		Status:        entity.StatusRunning,
		SSHHost:       sshHost,
		SSHPort:       sshPort,
		HourlyRate:    hourlyRate,
		NumGPUs:       offer.NumGPUs,
		ContextLength: contextLength,
	}
	if err := s.instances.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	return &CreateOnlyResult{Instance: inst, ServeCommand: s.engine.BuildRawCommand(model, offer.NumGPUs, contextLength)}, nil
}

// Start resumes a stopped instance and re-establishes its tunnel.
//
// Unlike Deploy this does NOT destroy the instance on failure: Deploy created the
// instance and therefore owns it, whereas here the user deliberately kept it
// alive. Tearing it down on a transient error would throw away the very thing
// they were paying storage to preserve. A failed Start leaves a billing GPU, so
// it says so loudly instead of cleaning up behind the user's back.
//
// vast.ai re-runs the onstart script on container start, so the model server
// comes back by itself and the GGUF is still in the container-disk cache — this
// is the one path that skips the download.
func (s *DeployService) Start(ctx context.Context, id int64) error {
	inst, err := s.instances.FindByID(id)
	if err != nil {
		return err
	}

	// The startup budget comes from the model when it's still in the catalog; a
	// row referencing a removed model still has to be startable. The lookup is
	// hoisted because the watcher wants the model too (for the download size);
	// a nil model there simply means progress is reported without a percentage.
	timeout := defaultStartupTimeout
	model, modelErr := s.models.FindByName(inst.ModelName)
	if modelErr != nil {
		model = &entity.Model{Name: inst.ModelName}
	} else if model.StartupTimeout > 0 {
		timeout = model.StartupTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fmt.Printf("Starting instance %d (%s)...\n", inst.VastaiID, inst.ModelName)
	if err := s.vastai.StartInstance(int(inst.VastaiID)); err != nil {
		return fmt.Errorf("start vast.ai instance: %w", err)
	}

	// From here the GPU is billing again. Every early return below must say so.
	warn := func(err error) error {
		fmt.Printf("\nWARNING: instance %d is running and billing, but the tunnel was not established.\n", inst.VastaiID)
		fmt.Printf("Retry with `mycodeagent start %d`, or destroy it with `mycodeagent kill %d`.\n", id, id)
		return err
	}

	fmt.Println("Waiting for instance to start...")
	// SSH host and port are reassigned on resume — never reuse the stored ones.
	sshHost, sshPort, hourlyRate, err := s.vastai.WaitForInstance(ctx, int(inst.VastaiID))
	if err != nil {
		return warn(fmt.Errorf("wait for instance: %w", err))
	}
	fmt.Printf("Instance running: SSH at %s:%d\n", sshHost, sshPort)

	fmt.Println("Waiting for SSH...")
	if err := s.ssh.WaitForSSH(ctx, sshHost, sshPort); err != nil {
		return warn(fmt.Errorf("wait for SSH: %w", err))
	}

	localPort, err := s.ssh.FindFreePort(s.basePort)
	if err != nil {
		return warn(fmt.Errorf("find free port: %w", err))
	}

	fmt.Printf("Starting SSH tunnel on port %d...\n", localPort)
	tunnelPID, err := s.ssh.StartTunnel(localPort, sshHost, sshPort)
	if err != nil {
		return warn(fmt.Errorf("start tunnel: %w", err))
	}

	inst.Status = entity.StatusRunning
	inst.SSHHost = sshHost
	inst.SSHPort = sshPort
	inst.LocalPort = localPort
	inst.TunnelPID = tunnelPID
	inst.HourlyRate = hourlyRate
	if err := s.instances.Update(inst); err != nil {
		return warn(fmt.Errorf("update instance: %w", err))
	}

	fmt.Println("Waiting for model server (weights are cached on the container disk, no download)...")
	healthCh := make(chan error, 1)
	failCh := make(chan error, 1)
	go func() { healthCh <- s.ssh.WaitForServerHealth(ctx, localPort) }()
	go s.watchServerProcess(ctx, model, sshHost, sshPort, failCh)

	select {
	case healthErr := <-healthCh:
		if healthErr != nil {
			return warn(fmt.Errorf("model server health check: %w", healthErr))
		}
	case crashErr := <-failCh:
		return warn(fmt.Errorf("model server crashed during startup: %w", crashErr))
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return warn(fmt.Errorf("start interrupted"))
		}
		return warn(fmt.Errorf("start timed out after %s", timeout))
	}

	fmt.Printf("\nAPI available at: http://localhost:%d/v1\n", localPort)
	return nil
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
	// The offer is gone by now, so both the GPU count and the context length come
	// from what was persisted at deploy time; the model definition is only the
	// fallback for legacy rows that predate those columns. Using the catalog
	// baseline instead would silently shrink the window on a rental whose GPUs
	// were fatter than the tier minimum.
	numGPUs := inst.NumGPUs
	if numGPUs <= 0 {
		numGPUs = model.NumGPUs
	}
	contextLength := inst.ContextLength
	if contextLength <= 0 {
		contextLength = model.ContextLength
	}
	fmt.Println("Updating startup script...")
	onstart := s.engine.BuildOnstart(model, numGPUs, contextLength, s.hfToken)
	// BuildOnstart returns: echo '...' > /tmp/script.sh && chmod +x ... && bash ...
	// Strip the final "&& bash ..." to only write the file without executing.
	// If the rewrite fails, stop: restarting would silently relaunch the old
	// script, which looks like a successful restart that changed nothing.
	idx := strings.LastIndex(onstart, " && bash ")
	if idx <= 0 {
		return fmt.Errorf("onstart script has no ' && bash ' separator to split on")
	}
	if out, err := s.ssh.RunRemoteCommand(inst.SSHHost, inst.SSHPort, onstart[:idx]); err != nil {
		return fmt.Errorf("write startup script: %w (%s)", err, strings.TrimSpace(string(out)))
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
