package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

func testDeployModel() *entity.Model {
	return &entity.Model{
		Name:             "test-model",
		Alias:            "t",
		HFRepo:           "unsloth/Test-GGUF",
		Quant:            "Q4_K_M",
		VRAM:             24,
		NumGPUs:          1,
		DiskGB:           30,
		StartupTimeout:   2 * time.Second,
		ContextLength:    32768,
		MaxContextLength: 262144,
	}
}

func newTestDeploy(model *entity.Model, vast *fakeVastai, ssh *fakeSSH, repo *fakeInstanceRepo) (*DeployService, *fakeEngine) {
	eng := &fakeEngine{}
	return NewDeployService(
		&fakeModelRepo{models: []*entity.Model{model}},
		repo,
		newFakeBadHostRepo(),
		vast, ssh, eng,
		8000, "", "",
	), eng
}

func oneOffer() []OfferResult {
	return []OfferResult{{ID: 7, GPUName: "RTX 3090", NumGPUs: 1, GPUMemory: 24576, DPHTotal: 0.11, MachineID: 555}}
}

// A deploy that dies after the instance row is written must leave NOTHING behind:
// no vast.ai instance, no tunnel, and no local row. The row delete is the part
// that regressed — the cleanup used to key off the named return value, which every
// `return nil, err` had already set to nil before the deferred function ran.
func TestDeployCleansUpLocalRowWhenHealthCheckFails(t *testing.T) {
	repo := newFakeInstanceRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 4242}
	ssh := &fakeSSH{tunnelPID: 31337, healthErr: context.DeadlineExceeded}

	svc, _ := newTestDeploy(testDeployModel(), vast, ssh, repo)

	inst, err := svc.Deploy(context.Background(), "test-model", DeployOptions{})
	if err == nil {
		t.Fatal("expected the deploy to fail")
	}
	if inst != nil {
		t.Errorf("failed deploy returned an instance: %+v", inst)
	}
	if got := repo.count(); got != 0 {
		t.Errorf("failed deploy left %d local row(s) behind; they inflate `budget` and make `config` write a dead provider", got)
	}
	if len(vast.destroyed) != 1 || vast.destroyed[0] != 4242 {
		t.Errorf("expected instance 4242 to be destroyed, got %v", vast.destroyed)
	}
	if stopped := ssh.stopped(); len(stopped) != 1 || stopped[0] != 31337 {
		t.Errorf("expected tunnel 31337 to be stopped, got %v", stopped)
	}
}

// Ctrl-C cancels the caller's context. That must run the same teardown as any
// other failure — otherwise interrupting a 15-minute deploy leaves a billing GPU.
func TestDeployDestroysInstanceOnContextCancel(t *testing.T) {
	repo := newFakeInstanceRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 99}
	ssh := &fakeSSH{tunnelPID: 1, healthWait: time.Minute} // never becomes healthy

	svc, _ := newTestDeploy(testDeployModel(), vast, ssh, repo)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := svc.Deploy(ctx, "test-model", DeployOptions{})
	if err == nil {
		t.Fatal("expected an error after cancellation")
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("cancellation should be reported as an interrupt, not a timeout: %v", err)
	}
	if len(vast.destroyed) != 1 {
		t.Errorf("interrupted deploy did not destroy the instance (still billing): %v", vast.destroyed)
	}
	if repo.count() != 0 {
		t.Errorf("interrupted deploy left %d local row(s)", repo.count())
	}
}

// A failure *before* the row is written must not try to delete row 0.
func TestDeployCleansUpWhenInstanceNeverStarts(t *testing.T) {
	repo := newFakeInstanceRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 11, waitErr: context.DeadlineExceeded}
	ssh := &fakeSSH{tunnelPID: 5}

	svc, _ := newTestDeploy(testDeployModel(), vast, ssh, repo)

	if _, err := svc.Deploy(context.Background(), "test-model", DeployOptions{}); err == nil {
		t.Fatal("expected the deploy to fail")
	}
	if repo.count() != 0 {
		t.Errorf("expected no local rows, got %d", repo.count())
	}
	if len(vast.destroyed) != 1 {
		t.Errorf("expected the instance to be destroyed, got %v", vast.destroyed)
	}
	if stopped := ssh.stopped(); len(stopped) != 0 {
		t.Errorf("no tunnel was started, so none should be stopped: %v", stopped)
	}
}

// The happy path must NOT tear anything down.
func TestDeploySuccessKeepsInstanceAndPersistsRuntimeShape(t *testing.T) {
	repo := newFakeInstanceRepo()
	// 48 GB per GPU against a 24 GB baseline ⇒ scaledContextLength doubles the window.
	vast := &fakeVastai{
		offers:    []OfferResult{{ID: 7, NumGPUs: 1, GPUMemory: 49152, DPHTotal: 0.4, MachineID: 1}},
		createdID: 77,
	}
	ssh := &fakeSSH{tunnelPID: 4242}

	svc, eng := newTestDeploy(testDeployModel(), vast, ssh, repo)

	inst, err := svc.Deploy(context.Background(), "test-model", DeployOptions{})
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if len(vast.destroyed) != 0 {
		t.Errorf("successful deploy destroyed the instance: %v", vast.destroyed)
	}
	if repo.count() != 1 {
		t.Fatalf("expected exactly 1 persisted row, got %d", repo.count())
	}
	if eng.lastContext != 65536 {
		t.Errorf("expected the scaled context (65536) to reach the engine, got %d", eng.lastContext)
	}
	// Restart re-reads these from the row, so they must match what was launched.
	if inst.ContextLength != eng.lastContext {
		t.Errorf("persisted ContextLength %d != launched %d", inst.ContextLength, eng.lastContext)
	}
	if inst.NumGPUs != 1 {
		t.Errorf("persisted NumGPUs = %d, want 1", inst.NumGPUs)
	}
}

// Restart must relaunch with the window the instance was actually deployed with.
// Falling back to the catalog baseline silently shrank the context on any rental
// whose GPUs were fatter than the tier minimum.
func TestRestartReusesPersistedContextLength(t *testing.T) {
	model := testDeployModel()
	repo := newFakeInstanceRepo(&entity.Instance{
		VastaiID: 5, ModelName: model.Name, Status: entity.StatusRunning,
		SSHHost: "h", SSHPort: 22, NumGPUs: 1, ContextLength: 131072,
	})
	svc, eng := newTestDeploy(model, &fakeVastai{}, &fakeSSH{}, repo)

	if err := svc.Restart(context.Background(), repo.ids()[0]); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if eng.lastContext != 131072 {
		t.Errorf("restart used context %d, want the persisted 131072 (catalog baseline is %d)",
			eng.lastContext, model.ContextLength)
	}
}

// Ctrl-C surfaces as context.Canceled from whichever wait was in flight. It says
// nothing about the machine, so it must not blacklist one — otherwise a session
// spent aborting slow deploys silently drains the offer pool one good host at a
// time. Observed live: machine 141581 was banned for an interrupted deploy.
func TestDeployDoesNotBlacklistOnUserCancel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		waitErr   error
		wantBlame bool
	}{
		{"user interrupted", context.Canceled, false},
		{"wrapped interrupt", fmt.Errorf("instance 1 did not start: %w", context.Canceled), false},
		{"genuine timeout", context.DeadlineExceeded, true},
		{"wrapped timeout", fmt.Errorf("instance 1 did not start: %w", context.DeadlineExceeded), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badHosts := newFakeBadHostRepo()
			vast := &fakeVastai{offers: oneOffer(), createdID: 1, waitErr: tc.waitErr}
			model := testDeployModel()
			svc := NewDeployService(
				&fakeModelRepo{models: []*entity.Model{model}},
				newFakeInstanceRepo(), badHosts, vast, &fakeSSH{}, &fakeEngine{}, 8000, "", "",
			)

			if _, err := svc.Deploy(context.Background(), model.Name, DeployOptions{}); err == nil {
				t.Fatal("expected the deploy to fail")
			}
			blamed := len(badHosts.added) > 0
			if blamed != tc.wantBlame {
				t.Errorf("blacklisted=%v, want %v (recorded: %v)", blamed, tc.wantBlame, badHosts.added)
			}
			// Either way the instance must be gone — cancelling still tears down.
			if len(vast.destroyed) != 1 {
				t.Errorf("instance not destroyed: %v", vast.destroyed)
			}
		})
	}
}

// A timeout is charged to the host only when the host had already burned an
// outlier share of the budget before the model was reached. Provisioning is the
// one phase no model can influence, which is what makes it admissible evidence.
func TestBlameHostForTimeout(t *testing.T) {
	for _, tc := range []struct {
		name         string
		provisioning time.Duration
		want         bool
	}{
		{"healthy host, seconds", 12 * time.Second, false},
		{"unremarkable", 2 * time.Minute, false},
		{"right at the threshold", slowProvisionThreshold, false},
		{"over the threshold", slowProvisionThreshold + time.Second, true},
		{"the machine that motivated this", 9*time.Minute + 18*time.Second, true},
	} {
		if got := blameHostForTimeout(tc.provisioning); got != tc.want {
			t.Errorf("%s: blameHostForTimeout(%s) = %v, want %v",
				tc.name, tc.provisioning, got, tc.want)
		}
	}
}

// A crashed server is the model's fault — a bad quant, a rejected flag, too
// little VRAM. Blaming the host for it is how a misconfigured catalog entry
// would blacklist every good machine one deploy at a time, ending in
// "no offers found". This must hold no matter how slow the host was.
func TestDeployNeverBlamesHostForACrash(t *testing.T) {
	// Shrink the watcher so the crash path is reachable; the real 90s grace plus
	// two 20s reads would make this a two-minute test.
	defer withFastLivenessWatcher()()

	repo := newFakeInstanceRepo()
	badHosts := newFakeBadHostRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 4242}
	// Liveness reports DEAD, so the watcher fires before the health poll can.
	ssh := &fakeSSH{
		tunnelPID:  1,
		healthWait: time.Minute,
		remoteOut:  map[string]string{"liveness": "DEAD"},
	}
	model := testDeployModel()
	model.StartupTimeout = 5 * time.Second // room for the watcher to reach 2 reads
	svc := NewDeployService(
		&fakeModelRepo{models: []*entity.Model{model}},
		repo, badHosts, vast, ssh, &fakeEngine{}, 8000, "", "",
	)

	_, err := svc.Deploy(context.Background(), model.Name, DeployOptions{})
	if err == nil {
		t.Fatal("expected the deploy to fail")
	}
	if len(badHosts.added) != 0 {
		t.Errorf("a model-side crash blacklisted machine(s) %v — this is how the "+
			"offer pool gets drained one good host at a time", badHosts.added)
	}
	if len(vast.destroyed) != 1 {
		t.Errorf("crashed deploy did not destroy the instance: %v", vast.destroyed)
	}
	if !strings.Contains(err.Error(), "crashed") {
		t.Errorf("expected the crash path, got a different failure: %v", err)
	}
}

// The case that motivated the feature: the host was healthy but pathologically
// slow, ate most of the budget provisioning, and the deploy died on the health
// timeout. Before this, that failure was classified model-side, so the machine
// stayed in the offer pool and the next deploy could pick it straight back up.
func TestDeployBlamesHostThatAteTheBudgetProvisioning(t *testing.T) {
	restore := slowProvisionThreshold
	slowProvisionThreshold = 20 * time.Millisecond
	defer func() { slowProvisionThreshold = restore }()

	repo := newFakeInstanceRepo()
	badHosts := newFakeBadHostRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 4242, waitDelay: 60 * time.Millisecond}
	ssh := &fakeSSH{tunnelPID: 1, healthErr: context.DeadlineExceeded}
	model := testDeployModel()
	svc := NewDeployService(
		&fakeModelRepo{models: []*entity.Model{model}},
		repo, badHosts, vast, ssh, &fakeEngine{}, 8000, "", "",
	)

	if _, err := svc.Deploy(context.Background(), model.Name, DeployOptions{}); err == nil {
		t.Fatal("expected the deploy to fail")
	}
	reason, blamed := badHosts.added[oneOffer()[0].MachineID]
	if !blamed {
		t.Fatalf("slow host was not blacklisted; recorded: %v", badHosts.added)
	}
	if !strings.Contains(reason, "provisioning") {
		t.Errorf("blame reason does not name the evidence: %q", reason)
	}
}

// The mirror image: a host that provisioned promptly is not blamed when the
// deploy times out later — that is the model taking too long, not the machine.
func TestDeployDoesNotBlameAPromptHostForATimeout(t *testing.T) {
	repo := newFakeInstanceRepo()
	badHosts := newFakeBadHostRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 4242} // provisions instantly
	ssh := &fakeSSH{tunnelPID: 1, healthErr: context.DeadlineExceeded}
	model := testDeployModel()
	svc := NewDeployService(
		&fakeModelRepo{models: []*entity.Model{model}},
		repo, badHosts, vast, ssh, &fakeEngine{}, 8000, "", "",
	)

	if _, err := svc.Deploy(context.Background(), model.Name, DeployOptions{}); err == nil {
		t.Fatal("expected the deploy to fail")
	}
	if len(badHosts.added) != 0 {
		t.Errorf("a prompt host was blacklisted for a slow model: %v", badHosts.added)
	}
}

// withFastLivenessWatcher shrinks the watcher cadence and returns a restore func.
func withFastLivenessWatcher() func() {
	grace, interval := livenessGraceNanos.Load(), livenessIntervalNanos.Load()
	livenessGraceNanos.Store(int64(10 * time.Millisecond))
	livenessIntervalNanos.Store(int64(10 * time.Millisecond))
	return func() {
		livenessGraceNanos.Store(grace)
		livenessIntervalNanos.Store(interval)
	}
}

// Start must re-read the SSH host/port: vast.ai reassigns them on resume, so
// reusing the stored ones would tunnel to whatever now occupies the old slot.
func TestStartRefreshesSSHAndReopensTunnel(t *testing.T) {
	model := testDeployModel()
	repo := newFakeInstanceRepo(&entity.Instance{
		VastaiID: 55, ModelName: model.Name, Status: entity.StatusStopped,
		SSHHost: "stale.host", SSHPort: 1111, LocalPort: 0, TunnelPID: 0,
	})
	vast := &fakeVastai{} // WaitForInstance returns ssh.example:2222
	ssh := &fakeSSH{tunnelPID: 777}
	svc, _ := newTestDeploy(model, vast, ssh, repo)

	id := repo.ids()[0]
	if err := svc.Start(context.Background(), id); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if len(vast.started) != 1 || vast.started[0] != 55 {
		t.Errorf("expected StartInstance(55), got %v", vast.started)
	}
	got, _ := repo.FindByID(id)
	if got.SSHHost == "stale.host" || got.SSHPort == 1111 {
		t.Errorf("start reused the stale SSH endpoint: %s:%d", got.SSHHost, got.SSHPort)
	}
	if got.SSHHost != "ssh.example" || got.SSHPort != 2222 {
		t.Errorf("SSH endpoint = %s:%d, want ssh.example:2222", got.SSHHost, got.SSHPort)
	}
	if got.TunnelPID != 777 || got.LocalPort != 8000 {
		t.Errorf("tunnel not recorded: pid=%d port=%d", got.TunnelPID, got.LocalPort)
	}
	if !got.Status.Is(entity.StatusRunning) {
		t.Errorf("status = %q, want running", got.Status)
	}
	if len(vast.destroyed) != 0 {
		t.Errorf("start must never destroy the instance: %v", vast.destroyed)
	}
}

// A failed Start must NOT destroy the instance — the user paid storage to keep
// it, and Start didn't create it. This is the deliberate difference from Deploy.
func TestStartDoesNotDestroyOnFailure(t *testing.T) {
	model := testDeployModel()
	repo := newFakeInstanceRepo(&entity.Instance{
		VastaiID: 55, ModelName: model.Name, Status: entity.StatusStopped,
	})
	vast := &fakeVastai{waitErr: context.DeadlineExceeded}
	svc, _ := newTestDeploy(model, vast, &fakeSSH{}, repo)

	if err := svc.Start(context.Background(), repo.ids()[0]); err == nil {
		t.Fatal("expected start to fail")
	}
	if len(vast.destroyed) != 0 {
		t.Errorf("start destroyed an instance the user chose to keep: %v", vast.destroyed)
	}
	if repo.count() != 1 {
		t.Errorf("start deleted the local row: %d rows left", repo.count())
	}
}

// Legacy rows predate the column and carry 0 — those fall back to the catalog.
func TestRestartFallsBackToCatalogContextForLegacyRows(t *testing.T) {
	model := testDeployModel()
	repo := newFakeInstanceRepo(&entity.Instance{
		VastaiID: 5, ModelName: model.Name, Status: entity.StatusRunning,
		SSHHost: "h", SSHPort: 22, NumGPUs: 0, ContextLength: 0,
	})
	svc, eng := newTestDeploy(model, &fakeVastai{}, &fakeSSH{}, repo)

	if err := svc.Restart(context.Background(), repo.ids()[0]); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if eng.lastContext != model.ContextLength {
		t.Errorf("legacy row should fall back to %d, got %d", model.ContextLength, eng.lastContext)
	}
	if eng.lastNumGPUs != model.NumGPUs {
		t.Errorf("legacy row should fall back to %d GPUs, got %d", model.NumGPUs, eng.lastNumGPUs)
	}
}

// The regression this guards: StartTunnel hardcoded the forward target to
// localhost:8000, llama-server's port. EngineProvider grew a ServerPort method
// for the ComfyUI (8188) and Jupyter (8888) engines, every engine implemented
// it, MultiEngine forwarded it — and nothing ever called it, so both new engine
// types got a tunnel aimed at a port with nothing behind it. The deploy then
// health-checked through that tunnel until the startup deadline and destroyed a
// container that was serving perfectly well.
func TestDeployForwardsTheTunnelToTheEnginesPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		port int
	}{
		{"llama.cpp", 8000},
		{"comfyui", 8188},
		{"jupyter", 8888},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeInstanceRepo()
			vast := &fakeVastai{offers: oneOffer(), createdID: 4242}
			ssh := &fakeSSH{tunnelPID: 31337}
			svc, eng := newTestDeploy(testDeployModel(), vast, ssh, repo)
			eng.port = tc.port

			if _, err := svc.Deploy(context.Background(), "test-model", DeployOptions{}); err != nil {
				t.Fatalf("deploy: %v", err)
			}
			if got := ssh.lastRemotePort(); got != tc.port {
				t.Errorf("tunnel forwards to remote port %d, want the engine's %d", got, tc.port)
			}
		})
	}
}

// Engine-supplied environment must actually reach the container. ComfyUI's
// "disable the web UI's own auth" variables were described in a comment and set
// nowhere, so the image's defaults applied behind a tunnel that is already the
// only access control.
func TestDeployPassesEngineEnvironmentToTheInstance(t *testing.T) {
	repo := newFakeInstanceRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 4242}
	ssh := &fakeSSH{tunnelPID: 31337}
	svc, eng := newTestDeploy(testDeployModel(), vast, ssh, repo)
	eng.env = map[string]string{"WEB_ENABLE_AUTH": "false"}

	if _, err := svc.Deploy(context.Background(), "test-model", DeployOptions{}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if got := vast.createdEnv["WEB_ENABLE_AUTH"]; got != "false" {
		t.Errorf("engine env did not reach CreateInstance: got %q, want \"false\"", got)
	}
}

// ...but a credential the user configured must win, so an engine cannot shadow
// the HF token by declaring the same key.
func TestDeployCredentialsOutrankEngineEnvironment(t *testing.T) {
	repo := newFakeInstanceRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 4242}
	ssh := &fakeSSH{tunnelPID: 31337}
	svc, eng := newTestDeploy(testDeployModel(), vast, ssh, repo)
	svc.hfToken = "real-token"
	eng.env = map[string]string{"HF_TOKEN": "engine-would-shadow-this"}

	if _, err := svc.Deploy(context.Background(), "test-model", DeployOptions{}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if got := vast.createdEnv["HF_TOKEN"]; got != "real-token" {
		t.Errorf("engine shadowed the configured HF token: got %q", got)
	}
}

// Instances are disposable and `kill` is the documented way to finish, so
// anything an engine *produces* has to be copied out while it still exists.
// That was free when llama.cpp was the only engine — it reads weights and
// writes nothing — and stopped being free the moment ComfyUI arrived.
func TestDeployStartsOutputSyncOnlyForEnginesThatProduce(t *testing.T) {
	dirs := []entity.SyncDir{{RemoteCandidates: []string{"/opt/ComfyUI/output"}, Local: "output"}}

	for _, tc := range []struct {
		name      string
		syncDirs  []entity.SyncDir
		wantStart int
		wantPID   int
	}{
		{"engine produces output", dirs, 1, 4242},
		{"engine produces nothing", nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeInstanceRepo()
			vast := &fakeVastai{offers: oneOffer(), createdID: 7}
			ssh := &fakeSSH{tunnelPID: 1, syncPID: 4242}
			model := testDeployModel()
			svc := NewDeployService(
				&fakeModelRepo{models: []*entity.Model{model}},
				repo, newFakeBadHostRepo(), vast, ssh,
				&fakeEngine{syncDirs: tc.syncDirs}, 8000, "", "",
			)

			inst, err := svc.Deploy(context.Background(), model.Name, DeployOptions{})
			if err != nil {
				t.Fatalf("deploy: %v", err)
			}
			if got := ssh.syncStartCount(); got != tc.wantStart {
				t.Errorf("StartSync called %d times, want %d", got, tc.wantStart)
			}
			if inst.SyncPID != tc.wantPID {
				t.Errorf("SyncPID = %d, want %d", inst.SyncPID, tc.wantPID)
			}
			// Whatever happened, the pid must be persisted, or stop/kill cannot
			// end the loop and every deploy leaks one.
			stored, _ := repo.FindByID(inst.ID)
			if stored.SyncPID != tc.wantPID {
				t.Errorf("persisted SyncPID = %d, want %d", stored.SyncPID, tc.wantPID)
			}
		})
	}
}

// A sync that outlives its instance keeps rsyncing against a destroyed host.
func TestDestroyStopsTheOutputSync(t *testing.T) {
	repo := newFakeInstanceRepo(&entity.Instance{
		VastaiID: 9, ModelName: "test-model", SSHHost: "h", SSHPort: 22,
		TunnelPID: 11, SyncPID: 4242,
	})
	ssh := &fakeSSH{}
	svc, _ := newTestDeploy(testDeployModel(), &fakeVastai{}, ssh, repo)

	if err := svc.Destroy(context.Background(), repo.ids()[0]); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	stopped := ssh.stoppedSyncPIDs()
	if len(stopped) != 1 || stopped[0] != 4242 {
		t.Errorf("sync loop not stopped on destroy: %v", stopped)
	}
}

// A deploy that succeeds but cannot start the sync must still be a success —
// the GPU is up and serving; refusing it would destroy a working instance over
// a missing rsync.
func TestDeploySucceedsWhenSyncCannotStart(t *testing.T) {
	repo := newFakeInstanceRepo()
	vast := &fakeVastai{offers: oneOffer(), createdID: 7}
	ssh := &fakeSSH{tunnelPID: 1, syncErr: fmt.Errorf("rsync not found on PATH")}
	model := testDeployModel()
	svc := NewDeployService(
		&fakeModelRepo{models: []*entity.Model{model}},
		repo, newFakeBadHostRepo(), vast, ssh,
		&fakeEngine{syncDirs: []entity.SyncDir{{RemoteCandidates: []string{"/x"}, Local: "output"}}},
		8000, "", "",
	)

	inst, err := svc.Deploy(context.Background(), model.Name, DeployOptions{})
	if err != nil {
		t.Fatalf("a failed sync must not fail the deploy: %v", err)
	}
	if len(vast.destroyed) != 0 {
		t.Errorf("instance destroyed over a sync failure: %v", vast.destroyed)
	}
	if inst.SyncPID != 0 {
		t.Errorf("SyncPID = %d, want 0 when the loop never started", inst.SyncPID)
	}
}

// Credentials this service owns must survive an engine declaring the same key.
// An engine that could shadow HF_TOKEN or CIVITAI_TOKEN would silently break
// gated downloads, and the failure would look like a bad token.
func TestEngineEnvCannotShadowCredentials(t *testing.T) {
	svc := NewDeployService(
		&fakeModelRepo{}, newFakeInstanceRepo(), newFakeBadHostRepo(),
		&fakeVastai{}, &fakeSSH{},
		&fakeEngine{env: map[string]string{
			"HF_TOKEN":            "engine-tries-to-win",
			"CIVITAI_TOKEN":       "engine-tries-to-win",
			"PROVISIONING_SCRIPT": "engine-tries-to-win",
			"WEB_ENABLE_AUTH":     "false",
		}},
		8000, "real-hf", "real-civitai",
	)

	env := svc.engineEnv(&entity.Model{}, DeployOptions{ProvisioningScript: "https://example/p.sh"})

	for k, want := range map[string]string{
		"HF_TOKEN":            "real-hf",
		"CIVITAI_TOKEN":       "real-civitai",
		"PROVISIONING_SCRIPT": "https://example/p.sh",
		"WEB_ENABLE_AUTH":     "false", // engine keys with no conflict survive
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
}

// Tokens that were never configured must not appear at all — an empty
// CIVITAI_TOKEN in the environment reads to a provisioning script as "a token
// was supplied" and turns a clean 401 into a confusing one.
func TestEngineEnvOmitsUnsetCredentials(t *testing.T) {
	svc := NewDeployService(
		&fakeModelRepo{}, newFakeInstanceRepo(), newFakeBadHostRepo(),
		&fakeVastai{}, &fakeSSH{}, &fakeEngine{}, 8000, "", "",
	)
	env := svc.engineEnv(&entity.Model{}, DeployOptions{})

	for _, k := range []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN", "CIVITAI_TOKEN", "PROVISIONING_SCRIPT"} {
		if _, present := env[k]; present {
			t.Errorf("%s present despite being unset: %q", k, env[k])
		}
	}
}
