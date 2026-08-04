package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// InstanceService owns all read/write/orchestration over instances. It is the
// home for everything App needs to expose to the command layer that touches a
// vast.ai instance — CRUD, sync, tunnel re-establishment, log fetching, server
// probing, and credential operations.
//
// It holds repository.InstanceRepository (for local DB CRUD), VastaiProvider
// (for vast.ai API), SSHTunnelProvider (for tunnel processes), ServerProbe (for
// localhost /v1/models reads), ModelService (so Sync can detect model names
// from remote onstart scripts), and the basePort the deploy stack uses for
// tunnel allocation.
type InstanceService struct {
	instances repository.InstanceRepository
	vastai    VastaiProvider
	ssh       SSHTunnelProvider
	probe     ServerProbe
	models    *ModelService
	engine    EngineProvider
	basePort  int
}

// BudgetSummary aggregates cost across running instances. Lines is the
// per-instance breakdown that the budget command needs; the totals stay for
// callers that only want the headline numbers (e.g. App.GetInfo).
type BudgetSummary struct {
	TotalHourlyRate  float64            `json:"total_hourly_rate"`
	TotalDayCost     float64            `json:"total_day_cost"`
	TotalMonthCost   float64            `json:"total_month_cost"`
	RunningInstances []*entity.Instance `json:"running_instances"`
	Lines            []BudgetLine       `json:"lines"`
}

// BudgetLine is one row in the budget command's table.
type BudgetLine struct {
	ID         int64
	ModelName  string
	Status     entity.InstanceStatus
	HourlyRate float64
	Hours      float64
	Cost       float64
}

func NewInstanceService(
	instances repository.InstanceRepository,
	vastai VastaiProvider,
	ssh SSHTunnelProvider,
	probe ServerProbe,
	models *ModelService,
	engine EngineProvider,
	basePort int,
) *InstanceService {
	return &InstanceService{
		instances: instances,
		vastai:    vastai,
		ssh:       ssh,
		probe:     probe,
		models:    models,
		engine:    engine,
		basePort:  basePort,
	}
}

// remotePort resolves the port the instance's service listens on.
//
// Falls back to the engine's default for an unknown model rather than failing:
// re-attaching a tunnel to an instance whose catalog entry has since been
// renamed should still work, and every engine answers ServerPort(nil-model) with
// its own default. Returning 0 is also safe — StartTunnel treats it as "use the
// llama.cpp port", which is what every pre-multi-engine row wants.
func (s *InstanceService) remotePort(modelName string) int {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return 0
	}
	return s.engine.ServerPort(model)
}

// servesOpenAIAPI reports whether a model's engine speaks the OpenAI protocol,
// and therefore whether /v1 means anything on it.
//
// A nil model counts as llama.cpp, as does an unset EngineType: rows written
// before the engine split have no type at all, and an instance whose catalog
// entry has since been renamed must stay reachable.
func servesOpenAIAPI(model *entity.Model) bool {
	return model == nil || model.EngineType == "" || model.EngineType == entity.EngineLlamaCpp
}

// servesOpenAIAPIByName is the same question asked about a stored instance,
// whose model is a name rather than an entry.
func (s *InstanceService) servesOpenAIAPIByName(modelName string) bool {
	model, err := s.models.FindByName(modelName)
	if err != nil {
		return true
	}
	return servesOpenAIAPI(model)
}

// TunnelURL is the address to open for an instance.
//
// llama.cpp serves an OpenAI-compatible API rooted at /v1, which is what a
// client needs. ComfyUI and Jupyter serve a web UI at the root and have no /v1
// at all, so printing one sent the operator to a 404 on a perfectly healthy
// instance. Same defect as the tunnel forwarding to llama.cpp's port regardless
// of engine: a value the EngineProvider already knew, hardcoded by the caller.
func (s *InstanceService) TunnelURL(inst *entity.Instance) string {
	if inst == nil || inst.LocalPort <= 0 {
		return ""
	}
	base := fmt.Sprintf("http://localhost:%d", inst.LocalPort)
	if s.servesOpenAIAPIByName(inst.ModelName) {
		return base + "/v1"
	}
	return base
}

// HealthProbe says how to check an instance through its tunnel: the URL to GET,
// and whether a 200 has to carry an OpenAI model list to count.
//
// The model list check is what distinguishes "loaded and serving" from "bound
// the port": llama-server answers 503 on every route but /health until the
// weights are in. It cannot be applied to the other engines, whose health routes
// answer with something else entirely — requiring it reported every healthy
// ComfyUI as unhealthy.
func (s *InstanceService) HealthProbe(inst *entity.Instance) (url string, expectModelList bool) {
	if inst == nil || inst.LocalPort <= 0 {
		return "", false
	}
	model, err := s.models.FindByName(inst.ModelName)
	if err != nil {
		model = nil // every engine answers a nil model with its own default
	}
	path := s.engine.HealthPath(model)
	return fmt.Sprintf("http://localhost:%d%s", inst.LocalPort, path), s.servesOpenAIAPIByName(inst.ModelName)
}

// ============================================================================
// Repository CRUD pass-throughs (so App stops touching the repo directly)
// ============================================================================

func (s *InstanceService) Save(ctx context.Context, inst *entity.Instance) error {
	return s.instances.Save(inst)
}

func (s *InstanceService) Update(ctx context.Context, inst *entity.Instance) error {
	return s.instances.Update(inst)
}

func (s *InstanceService) Delete(ctx context.Context, id int64) error {
	return s.instances.Delete(id)
}

func (s *InstanceService) FindByVastaiID(ctx context.Context, vastaiID int64) (*entity.Instance, error) {
	return s.instances.FindByVastaiID(vastaiID)
}

func (s *InstanceService) ListInstances(ctx context.Context) ([]*entity.Instance, error) {
	return s.instances.FindAll()
}

func (s *InstanceService) FindInstanceByID(ctx context.Context, id int64) (*entity.Instance, error) {
	return s.instances.FindByID(id)
}

// ============================================================================
// Tunnel operations
// ============================================================================

// StartTunnel is the low-level "start an SSH tunnel process" primitive used by
// the future-API App methods. EstablishTunnel is the high-level entry point
// that the tunnel command actually calls.
func (s *InstanceService) StartTunnel(ctx context.Context, instanceID int64, localPort int) (int, error) {
	inst, err := s.instances.FindByID(instanceID)
	if err != nil {
		return 0, fmt.Errorf("find instance: %w", err)
	}

	if localPort <= 0 {
		port, err := s.ssh.FindFreePort(s.basePort)
		if err != nil {
			return 0, fmt.Errorf("find free port: %w", err)
		}
		localPort = port
	}

	pid, err := s.ssh.StartTunnel(localPort, inst.SSHHost, inst.SSHPort, s.remotePort(inst.ModelName))
	if err != nil {
		return 0, fmt.Errorf("start tunnel: %w", err)
	}

	inst.TunnelPID = pid
	if err := s.instances.Update(inst); err != nil {
		return 0, fmt.Errorf("update instance with tunnel PID: %w", err)
	}

	return localPort, nil
}

func (s *InstanceService) StopTunnel(ctx context.Context, instanceID int64) error {
	inst, err := s.instances.FindByID(instanceID)
	if err != nil {
		return err
	}

	if inst.TunnelPID > 0 {
		if err := s.ssh.StopTunnel(inst.TunnelPID); err != nil {
			fmt.Printf("Warning: failed to stop tunnel: %v\n", err)
		}
	}

	inst.TunnelPID = 0
	return s.instances.Update(inst)
}

// EstablishTunnel is the full "re-attach to an existing instance" flow used by
// the tunnel command. It looks up the instance by vast.ai ID (not local ID),
// kills any stale tunnel, refreshes SSH info from vast.ai, waits for SSH,
// allocates a free local port, starts the tunnel process, and persists the
// updated instance row. Returns the updated instance for the command to print.
func (s *InstanceService) EstablishTunnel(ctx context.Context, vastaiID int64) (*entity.Instance, error) {
	inst, err := s.instances.FindByVastaiID(vastaiID)
	if err != nil {
		return nil, err
	}

	// Kill old tunnel if still referenced.
	if inst.TunnelPID > 0 {
		_ = s.ssh.StopTunnel(inst.TunnelPID)
	}

	// Refresh SSH info from vast.ai.
	remote, err := s.vastai.GetInstance(ctx, int(inst.VastaiID))
	if err != nil {
		return nil, fmt.Errorf("fetch instance from vast.ai: %w", err)
	}
	if remote.ActualStatus != "running" {
		return nil, fmt.Errorf("instance is %s, not running", remote.ActualStatus)
	}

	sshHost := remote.SSHHost
	if sshHost == "" {
		sshHost = remote.PublicIPAddr
	}
	sshPort := remote.SSHPort
	if sshHost == "" || sshPort == 0 {
		return nil, fmt.Errorf("SSH info not available (host=%s port=%d)", sshHost, sshPort)
	}

	// Wait for SSH (bounded internally by ctx).
	tunnelCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.ssh.WaitForSSH(tunnelCtx, sshHost, sshPort); err != nil {
		return nil, fmt.Errorf("SSH not reachable: %w", err)
	}

	localPort, err := s.ssh.FindFreePort(s.basePort)
	if err != nil {
		return nil, err
	}

	pid, err := s.ssh.StartTunnel(localPort, sshHost, sshPort, s.remotePort(inst.ModelName))
	if err != nil {
		return nil, fmt.Errorf("start tunnel: %w", err)
	}

	inst.SSHHost = sshHost
	inst.SSHPort = sshPort
	inst.LocalPort = localPort
	inst.TunnelPID = pid
	inst.Status = entity.StatusRunning

	// The output sync is restarted alongside the tunnel, not left running.
	// `tunnel` is what you reach for after a dropped connection, and the old
	// loop is either dead for the same reason or still pointed at the SSH
	// endpoint vast.ai has since reassigned — either way it is copying nothing.
	s.restartSync(inst)

	if err := s.instances.Update(inst); err != nil {
		return nil, fmt.Errorf("update instance: %w", err)
	}

	return inst, nil
}

// restartSync stops any previous loop and starts a fresh one for the instance's
// current SSH endpoint. Best-effort: a tunnel that works is worth more than a
// sync that does not, so failures warn rather than abort.
func (s *InstanceService) restartSync(inst *entity.Instance) {
	if inst.SyncPID > 0 {
		_ = s.ssh.StopSync(inst.SyncPID)
		inst.SyncPID = 0
	}
	model, err := s.models.FindByName(inst.ModelName)
	if err != nil {
		return // model no longer in the catalog; nothing to know about its output
	}
	dirs := s.engine.SyncDirs(model)
	if len(dirs) == 0 {
		return
	}
	// The root the deploy chose, not one derived from this command's working
	// directory. `tunnel` and `start` are routinely run from somewhere else, and
	// re-deriving would strand the rest of the instance's files in the old
	// directory. Empty on pre-SyncRoot rows, which the provider turns back into
	// the cwd default those loops are already using.
	pid, root, err := s.ssh.StartSync(inst.SSHHost, inst.SSHPort, dirs, inst.SyncRoot)
	if err != nil {
		fmt.Printf("Warning: output sync not restarted: %v\n", err)
		return
	}
	inst.SyncPID = pid
	inst.SyncRoot = root
	fmt.Printf("Output sync restarted -> %s\n", root)
}

// ============================================================================
// Log access
// ============================================================================

// GetVastaiLogs fetches the vast.ai-side log (the onstart bootstrap output that
// vast.ai uploads to S3 when requested). This is what `mycodeagent log <id>`
// has always shown. The adapter handles the URL fetch + retry loop.
func (s *InstanceService) GetVastaiLogs(ctx context.Context, instanceID int64, tail string) ([]byte, error) {
	inst, err := s.instances.FindByID(instanceID)
	if err != nil {
		return nil, fmt.Errorf("find instance: %w", err)
	}
	return s.vastai.GetInstanceLogs(ctx, int(inst.VastaiID), tail)
}

// ============================================================================
// Server probe
// ============================================================================

// GetServedModelInfo asks the localhost /v1/models endpoint what model is
// loaded and what its max context length is. Best-effort: returns ("", 0, nil)
// if the server is unreachable, since the probe is for display, not control.
func (s *InstanceService) GetServedModelInfo(ctx context.Context, localPort int) (string, int, error) {
	return s.probe.GetServedModel(ctx, localPort)
}

// ============================================================================
// Offer search
// ============================================================================

// SearchOffers returns the (sorted) list of available vast.ai offers for the
// model's GPU/VRAM requirements. Used by the models command for the price column.
func (s *InstanceService) SearchOffers(ctx context.Context, model *entity.Model) ([]OfferResult, error) {
	numGPUs := model.NumGPUs
	if numGPUs <= 0 {
		numGPUs = 1
	}
	return s.vastai.SearchOffers(model.VRAM, numGPUs, diskFor(model)+diskHeadroomGB, nil)
}

// ============================================================================
// Sync — pull from vast.ai, reconcile with local DB
// ============================================================================

// Sync pulls all running vast.ai instances and reconciles them with the local
// SQLite state: deduplicates local rows pointing at the same remote instance,
// updates status/SSH info on rows that match, inserts rows for newly seen
// remote instances (with model name detected from the onstart script), and
// deletes local rows whose remote instance is gone (after killing any stale
// tunnel process). Returns the reconciled local list. Replaces the inline
// reconcile loop that used to live in commands/ps.go.
func (s *InstanceService) Sync(ctx context.Context) ([]*entity.Instance, error) {
	remoteInstances, err := s.vastai.ListRemoteInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch instances from vast.ai: %w", err)
	}

	remoteMap := make(map[int64]*RemoteInstance, len(remoteInstances))
	for _, ri := range remoteInstances {
		remoteMap[int64(ri.VastaiID)] = ri
	}

	localInstances, err := s.instances.FindAll()
	if err != nil {
		return nil, fmt.Errorf("read local instances: %w", err)
	}

	// Deduplicate local rows pointing at the same vast.ai instance, keeping the
	// row that has a real LocalPort. Exactly one row per vast.ai id survives and
	// every other row is deleted exactly once — the winner must never end up in
	// the delete list, or the instance disappears from `ps` entirely and the
	// update pass below writes to a row that no longer exists.
	localByVastID := make(map[int64]*entity.Instance, len(localInstances))
	var dupes []*entity.Instance
	for _, li := range localInstances {
		existing, ok := localByVastID[li.VastaiID]
		if !ok {
			localByVastID[li.VastaiID] = li
			continue
		}
		keep, drop := existing, li
		if li.LocalPort > 0 && existing.LocalPort == 0 {
			keep, drop = li, existing
		}
		localByVastID[li.VastaiID] = keep
		dupes = append(dupes, drop)
	}
	for _, d := range dupes {
		// The row is going away, so nothing will ever reference its tunnel PID
		// again — kill it rather than leaking the ssh process.
		if d.TunnelPID > 0 && d.TunnelPID != localByVastID[d.VastaiID].TunnelPID {
			_ = s.ssh.StopTunnel(d.TunnelPID)
		}
		_ = s.instances.Delete(d.ID)
	}

	// Pre-load the static catalog once so detectModelFromOnstart doesn't
	// re-query the repo for every remote row.
	allModels, _ := s.models.List()

	// Update existing local rows; insert new ones for unseen remote instances.
	for _, ri := range remoteInstances {
		vastID := int64(ri.VastaiID)
		status := ri.ActualStatus
		if ri.CurState == "stopped" || ri.CurState == "exited" {
			status = ri.CurState
		}
		if ri.StatusMsg != "" {
			status = fmt.Sprintf("%s (%s)", status, ri.StatusMsg)
		}

		if local, ok := localByVastID[vastID]; ok {
			local.Status = entity.InstanceStatus(status)
			if ri.SSHHost != "" {
				local.SSHHost = ri.SSHHost
			}
			if ri.SSHPort > 0 {
				local.SSHPort = ri.SSHPort
			}
			local.HourlyRate = ri.HourlyRate
			if err := s.instances.Update(local); err != nil {
				fmt.Printf("update instance %d: %v\n", local.ID, err)
			}
			continue
		}

		inst := &entity.Instance{
			VastaiID:   vastID,
			ModelName:  detectModelFromOnstart(ri.Onstart, allModels),
			Status:     entity.InstanceStatus(status),
			SSHHost:    ri.SSHHost,
			SSHPort:    ri.SSHPort,
			HourlyRate: ri.HourlyRate,
		}
		if err := s.instances.Save(inst); err != nil {
			fmt.Printf("save instance %d: %v\n", vastID, err)
		}
	}

	// Delete local rows whose remote instance no longer exists.
	for _, li := range localByVastID {
		if _, stillRemote := remoteMap[li.VastaiID]; stillRemote {
			continue
		}
		if li.TunnelPID > 0 {
			// Via the provider, not syscall directly — the domain layer must not
			// reach for OS primitives it has an interface for.
			_ = s.ssh.StopTunnel(li.TunnelPID)
		}
		if err := s.instances.Delete(li.ID); err != nil {
			fmt.Printf("delete stale instance %d: %v\n", li.ID, err)
		}
	}

	return s.instances.FindAll()
}

// onstartScriptMarker is the start-script filename an engine writes into its
// onstart, and the only thing in that script identifying the engine when there
// is no model repo to match on.
//
// The names mirror the engine implementations, which this package must not
// import (domain does not depend on infrastructure).
// TestOnstartMarkersMatchTheEngineScripts, in the engine package, asserts each
// engine's onstart actually contains the marker named here, so the two cannot
// drift apart silently.
func onstartScriptMarker(e entity.EngineType) string {
	switch e {
	case entity.EngineJupyter:
		return "start_lab.sh"
	case entity.EngineComfyUI:
		return "start_cui.sh"
	case entity.EngineLlamaCpp:
		return "start_llama.sh"
	}
	return ""
}

// detectModelFromOnstart identifies the model an already-running instance is
// serving, for instances that exist on vast.ai but not in the local database.
//
// Matching is by HFRepo first, then by the engine's start-script name, then by
// the "-hf <repo>[:quant]" reference. Pure helper, no I/O.
//
// The empty-HFRepo guard is the whole point of the first loop's shape:
// strings.Contains(s, "") is true for every s, so a catalog entry with no repo
// matches every onstart ever written. ComfyUI and both Jupyter entries have no
// repo, and ComfyUI is first in the catalog — so every instance, whatever it
// was actually running, came back labelled "comfyui". Observed live: a deploy
// that had selected a 32 GB card and was pulling vastai/pytorch — indisputably
// the Jupyter spec — listed as ALIAS comfyui, MODEL comfyui.
//
// Models sharing an engine (jupyter-pytorch and jupyter-pytorch-16g) write
// identical scripts and cannot be told apart here; the first match wins. That
// is a wrong VRAM figure in `ps`, not a wrong engine, and the instance created
// through this CLI carries the exact name from its deploy anyway.
func detectModelFromOnstart(onstart string, models []*entity.Model) string {
	for _, m := range models {
		if m.HFRepo != "" && strings.Contains(onstart, m.HFRepo) {
			return m.Name
		}
	}
	for _, m := range models {
		if marker := onstartScriptMarker(m.EngineType); marker != "" &&
			strings.Contains(onstart, marker) {
			return m.Name
		}
	}
	if idx := strings.Index(onstart, "-hf "); idx >= 0 {
		ref := strings.Fields(onstart[idx+len("-hf "):])
		if len(ref) > 0 {
			return ref[0]
		}
	}
	return "unknown"
}

// ============================================================================
// Budget
// ============================================================================

// GetBudget returns aggregate spend numbers plus a per-instance breakdown for
// the budget command. Stopped instances contribute 0 hours per current behavior
// (we don't have historical hour-tracking — only "running since CreatedAt").
func (s *InstanceService) GetBudget(ctx context.Context) (*BudgetSummary, error) {
	instances, err := s.instances.FindAll()
	if err != nil {
		return nil, fmt.Errorf("find all instances: %w", err)
	}

	var activeInstances []*entity.Instance
	var totalHourlyRate float64
	var lines []BudgetLine
	var grandTotal float64

	for _, inst := range instances {
		hours := time.Since(inst.CreatedAt).Hours()
		// Status.Is, not ==: Sync records a vast.ai status_msg as a " (detail)"
		// suffix, so an exact match drops "running (loading)" out of the totals
		// and keeps billing "stopped (…)" rows as if they were live.
		if inst.Status.Is(entity.StatusStopped) {
			hours = 0
		}
		cost := inst.HourlyRate * hours
		grandTotal += cost
		lines = append(lines, BudgetLine{
			ID:         inst.ID,
			ModelName:  inst.ModelName,
			Status:     inst.Status,
			HourlyRate: inst.HourlyRate,
			Hours:      hours,
			Cost:       cost,
		})

		if inst.Status.Is(entity.StatusRunning) {
			activeInstances = append(activeInstances, inst)
			totalHourlyRate += inst.HourlyRate
		}
	}

	return &BudgetSummary{
		TotalHourlyRate:  totalHourlyRate,
		TotalDayCost:     totalHourlyRate * 24,
		TotalMonthCost:   totalHourlyRate * 24 * 30,
		RunningInstances: activeInstances,
		Lines:            lines,
	}, nil
}

// ============================================================================
// Credentials (login flow)
// ============================================================================

// VerifyVastaiKey checks that the supplied API key works against vast.ai.
// Used by the login command before the new key is persisted to config.
func (s *InstanceService) VerifyVastaiKey(ctx context.Context, apiKey string) error {
	return s.vastai.VerifyAPIKey(ctx, apiKey)
}

// ListVastaiSSHKeys returns the public_key strings registered on the vast.ai
// account belonging to the given API key.
func (s *InstanceService) ListVastaiSSHKeys(ctx context.Context, apiKey string) ([]string, error) {
	return s.vastai.ListSSHKeys(ctx, apiKey)
}

// UploadSSHKey uploads pubKey to the vast.ai account belonging to apiKey,
// but only if it isn't already registered. Returns whether an upload happened
// and how many keys are now on the remote account, so the login command can
// print a useful message.
func (s *InstanceService) UploadSSHKey(ctx context.Context, apiKey string, pubKey string) (uploaded bool, totalKeys int, err error) {
	keys, err := s.vastai.ListSSHKeys(ctx, apiKey)
	if err != nil {
		return false, 0, err
	}

	// Compare just the key data field (second whitespace-separated token),
	// since users sometimes have different comments on the same key.
	localKeyData := ""
	if fields := strings.Fields(pubKey); len(fields) >= 2 {
		localKeyData = fields[1]
	}

	for _, k := range keys {
		if fields := strings.Fields(k); len(fields) >= 2 && fields[1] == localKeyData {
			return false, len(keys), nil
		}
	}

	if err := s.vastai.CreateSSHKey(ctx, apiKey, pubKey); err != nil {
		return false, len(keys), err
	}
	return true, len(keys) + 1, nil
}
