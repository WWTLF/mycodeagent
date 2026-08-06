// Package application is the orchestration layer between CLI commands and
// domain services. App holds service references (never repositories or
// infrastructure clients) and exposes one method per use case. Methods are
// thin: they either delegate to a single service call, or compose a small
// number of service calls into a multi-step workflow.
//
// Layering rule (enforced by code review and the verification grep checks):
//
//	commands → application.App → domain/service → domain/repository
//
// App must NEVER:
//   - import any package under internal/infrastructure/
//   - call repository.* directly
//   - construct vastai.Client, ssh.Adapter, or any other infrastructure type
//   - call infrastructure helpers like config.Save()
//
// Credential persistence goes through service.CredentialStore (a domain
// interface, implemented under infrastructure/config). Read-side access to
// the current vast.ai key and HF token is via the VastaiAPIKey() / HFToken()
// accessors so commands never need a *config.Config reference.
package application

import (
	"context"
	"fmt"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/service"
)

type App struct {
	DeploySvc   *service.DeployService
	InstanceSvc *service.InstanceService
	ModelSvc    *service.ModelService
	BadHostSvc  *service.BadHostService
	credentials service.CredentialStore

	// In-memory copy of the credentials loaded at startup. Updated by Login
	// when the user changes them. Exposed via VastaiAPIKey()/HFToken().
	vastaiKey    string
	hfToken      string
	civitaiToken string
}

func NewApp(
	deploySvc *service.DeployService,
	instanceSvc *service.InstanceService,
	modelSvc *service.ModelService,
	badHostSvc *service.BadHostService,
	credentials service.CredentialStore,
	vastaiKey string,
	hfToken string,
	civitaiToken string,
) *App {
	return &App{
		DeploySvc:    deploySvc,
		InstanceSvc:  instanceSvc,
		ModelSvc:     modelSvc,
		BadHostSvc:   badHostSvc,
		credentials:  credentials,
		vastaiKey:    vastaiKey,
		hfToken:      hfToken,
		civitaiToken: civitaiToken,
	}
}

// VastaiAPIKey returns the vast.ai API key currently held in memory. Used by
// the login command to display the existing (masked) key before prompting for
// a replacement.
func (app *App) VastaiAPIKey() string { return app.vastaiKey }

// HFToken returns the HuggingFace token currently held in memory. Used by the
// login command to display the existing (masked) token before prompting for a
// replacement.
func (app *App) HFToken() string { return app.hfToken }

// CivitaiToken returns the CivitAI token held in memory, for the login command
// to display masked before prompting for a replacement.
func (app *App) CivitaiToken() string { return app.civitaiToken }

// ============================================================================
// Deploy lifecycle
// ============================================================================

// Deploy rents a GPU and brings the model up. opts carries the optional
// per-invocation choices: which countries to rent in, and a provisioning script
// URL for engines that fetch their own models.
func (app *App) Deploy(ctx context.Context, modelName string, opts service.DeployOptions) (*entity.Instance, error) {
	return app.DeploySvc.Deploy(ctx, modelName, opts)
}

func (app *App) DeployCreateOnly(ctx context.Context, modelName string, opts service.DeployOptions) (*service.CreateOnlyResult, error) {
	return app.DeploySvc.DeployCreateOnly(ctx, modelName, opts)
}

func (app *App) Stop(ctx context.Context, id int64) error {
	if err := app.DeploySvc.Stop(ctx, id); err != nil {
		return err
	}
	return app.InstanceSvc.StopTunnel(ctx, id)
}

// Start resumes a stopped instance and re-establishes its tunnel. The inverse of
// Stop — without it, `stop` would be a one-way door out of this CLI. localPort
// pins the local end of the new tunnel; zero takes the next free one.
func (app *App) Start(ctx context.Context, id int64, localPort int) error {
	return app.DeploySvc.Start(ctx, id, localPort)
}

func (app *App) Destroy(ctx context.Context, id int64) error {
	return app.DeploySvc.Destroy(ctx, id)
}

func (app *App) Restart(ctx context.Context, id int64) error {
	return app.DeploySvc.Restart(ctx, id)
}

// ============================================================================
// Instance read/write — clean delegations to InstanceService
// ============================================================================

func (app *App) ListInstances(ctx context.Context) ([]*entity.Instance, error) {
	return app.InstanceSvc.ListInstances(ctx)
}

func (app *App) FindInstanceByVastaiID(ctx context.Context, vastaiID int64) (*entity.Instance, error) {
	return app.InstanceSvc.FindByVastaiID(ctx, vastaiID)
}

func (app *App) FindInstanceByID(ctx context.Context, id int64) (*entity.Instance, error) {
	return app.InstanceSvc.FindInstanceByID(ctx, id)
}

func (app *App) SaveInstance(ctx context.Context, inst *entity.Instance) error {
	return app.InstanceSvc.Save(ctx, inst)
}

func (app *App) UpdateInstance(ctx context.Context, inst *entity.Instance) error {
	return app.InstanceSvc.Update(ctx, inst)
}

func (app *App) DeleteInstance(ctx context.Context, id int64) error {
	return app.InstanceSvc.Delete(ctx, id)
}

// SyncInstances pulls all running instances from vast.ai, reconciles them with
// the local DB, and returns the reconciled list. Used by `mycodeagent ps`.
func (app *App) SyncInstances(ctx context.Context) ([]*entity.Instance, error) {
	return app.InstanceSvc.Sync(ctx)
}

// EstablishTunnel re-attaches an SSH tunnel to an existing vast.ai instance.
// Used by `mycodeagent tunnel <vastai_id>`. localPort pins the local end; zero
// takes the next free one.
func (app *App) EstablishTunnel(ctx context.Context, vastaiID int64, localPort int) (*entity.Instance, error) {
	return app.InstanceSvc.EstablishTunnel(ctx, vastaiID, localPort)
}

// GetServedModelInfo reads the localhost /v1/models endpoint of a running
// instance and returns the model id and max_model_len it reports. Used by
// `ps`, `config`, and `tunnel` for display and for opencode config writing.
func (app *App) GetServedModelInfo(ctx context.Context, localPort int) (string, int, error) {
	return app.InstanceSvc.GetServedModelInfo(ctx, localPort)
}

// TunnelURL is the address to open for an instance: /v1 for the OpenAI-speaking
// engines, the bare root for the ones that serve a web UI.
func (app *App) TunnelURL(inst *entity.Instance) string {
	return app.InstanceSvc.TunnelURL(inst)
}

// HealthProbe returns the URL to GET to check an instance, and whether a 200
// must carry an OpenAI model list to count as healthy.
func (app *App) HealthProbe(inst *entity.Instance) (url string, expectModelList bool) {
	return app.InstanceSvc.HealthProbe(inst)
}

// GetLogs returns the vast.ai-side onstart bootstrap log for an instance.
// This is the canonical "what does `mycodeagent log` show" path. The internal
// fetch+retry against the vast.ai S3 URL lives in InstanceSvc.GetVastaiLogs.
func (app *App) GetLogs(ctx context.Context, instanceID int64, tail string) ([]byte, error) {
	return app.InstanceSvc.GetVastaiLogs(ctx, instanceID, tail)
}

// GetBudget returns aggregate spend numbers plus per-instance breakdown.
func (app *App) GetBudget(ctx context.Context) (*service.BudgetSummary, error) {
	return app.InstanceSvc.GetBudget(ctx)
}

// SearchOffers returns the cheapest available vast.ai offers for the given
// model's GPU/VRAM requirements. Used by `mycodeagent models` for the price
// column.
func (app *App) SearchOffers(ctx context.Context, model *entity.Model) ([]service.OfferResult, error) {
	return app.InstanceSvc.SearchOffers(ctx, model)
}

// ============================================================================
// Model catalog
// ============================================================================

func (app *App) ListModels() ([]*entity.Model, error) {
	return app.ModelSvc.List()
}

func (app *App) FindModelByName(name string) (*entity.Model, error) {
	return app.ModelSvc.FindByName(name)
}

// ============================================================================
// Bad host management
// ============================================================================

func (app *App) ListBadHosts() ([]*entity.BadHost, error) {
	return app.BadHostSvc.List()
}

func (app *App) RemoveBadHost(machineID int) error {
	return app.BadHostSvc.Remove(machineID)
}

func (app *App) ClearBadHosts() error {
	return app.BadHostSvc.Clear()
}

// ============================================================================
// Login / credentials
// ============================================================================

// LoginInput is the bundle of credentials and options accepted by App.Login.
// VastaiKey and HFToken are the new values to persist (empty means "leave the
// existing value unchanged"). UploadSSHPubKey, when non-empty, asks Login to
// upload the local SSH public key to the vast.ai account if it isn't already
// registered.
type LoginInput struct {
	VastaiKey       string
	HFToken         string
	CivitaiToken    string
	UploadSSHPubKey string
}

// LoginResult tells the caller what happened so the command can print
// appropriate feedback to the user.
type LoginResult struct {
	KeyVerified     bool
	SSHKeyUploaded  bool
	SSHKeysOnRemote int
}

// Login validates the new vast.ai key, optionally uploads an SSH key, and
// persists the configuration via the injected CredentialStore. The TUI
// prompting stays in commands/login.go; this method is the headless equivalent.
func (app *App) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	result := &LoginResult{}

	if in.VastaiKey != "" {
		if err := app.InstanceSvc.VerifyVastaiKey(ctx, in.VastaiKey); err != nil {
			return result, fmt.Errorf("vast.ai API key verification failed: %w", err)
		}
		result.KeyVerified = true
	}

	if in.UploadSSHPubKey != "" && in.VastaiKey != "" {
		uploaded, total, err := app.InstanceSvc.UploadSSHKey(ctx, in.VastaiKey, in.UploadSSHPubKey)
		if err != nil {
			return result, fmt.Errorf("upload SSH key: %w", err)
		}
		result.SSHKeyUploaded = uploaded
		result.SSHKeysOnRemote = total
	} else if in.VastaiKey != "" {
		// Even when not uploading, report how many SSH keys are on the remote
		// account so the command can show "you have N keys" feedback.
		keys, err := app.InstanceSvc.ListVastaiSSHKeys(ctx, in.VastaiKey)
		if err == nil {
			result.SSHKeysOnRemote = len(keys)
		}
	}

	if err := app.credentials.SaveCredentials(in.VastaiKey, in.HFToken, in.CivitaiToken); err != nil {
		return result, fmt.Errorf("save credentials: %w", err)
	}

	// Update the in-memory copy so any subsequent App method in the same
	// process sees the new values. (login is normally a one-shot CLI run, so
	// this rarely matters in practice — but it keeps the in-memory state
	// consistent with what's on disk.)
	if in.VastaiKey != "" {
		app.vastaiKey = in.VastaiKey
	}
	if in.HFToken != "" {
		app.hfToken = in.HFToken
	}
	if in.CivitaiToken != "" {
		app.civitaiToken = in.CivitaiToken
	}

	return result, nil
}

// ============================================================================
// Future API (defined for completeness, not yet wired to a command)
// ============================================================================

// future API: not yet wired to a command. Kept as a documented orchestration
// surface so future "kill all" or scripted-cleanup features have a method to
// call. Uses Destroy under the hood.
func (app *App) Kill(ctx context.Context) error {
	instances, err := app.InstanceSvc.ListInstances(ctx)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}
	for _, inst := range instances {
		if err := app.Destroy(ctx, inst.ID); err != nil {
			fmt.Printf("Warning: failed to destroy instance %d: %v\n", inst.ID, err)
		}
	}
	return nil
}

// future API: not yet wired to a command.
func (app *App) GetInfo(ctx context.Context) (map[string]interface{}, error) {
	budget, err := app.InstanceSvc.GetBudget(ctx)
	if err != nil {
		return nil, fmt.Errorf("get budget: %w", err)
	}
	return map[string]interface{}{
		"running_instances": len(budget.RunningInstances),
		"total_hourly_rate": budget.TotalHourlyRate,
		"total_day_cost":    budget.TotalDayCost,
		"total_month_cost":  budget.TotalMonthCost,
	}, nil
}

// future API: not yet wired to a command. Low-level "start an SSH tunnel
// process for an existing instance row" — EstablishTunnel is the high-level
// path used by the tunnel command.
func (app *App) StartTunnel(ctx context.Context, instanceID int64, port int) (int, error) {
	return app.InstanceSvc.StartTunnel(ctx, instanceID, port)
}

// future API: not yet wired to a command.
func (app *App) StopTunnel(ctx context.Context, instanceID int64) error {
	return app.InstanceSvc.StopTunnel(ctx, instanceID)
}
