package service

import (
	"context"
	"testing"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

func newTestInstanceSvc(repo *fakeInstanceRepo, vast *fakeVastai, ssh *fakeSSH) *InstanceService {
	return newTestInstanceSvcWithEngine(repo, vast, ssh, &fakeEngine{})
}

func newTestInstanceSvcWithEngine(repo *fakeInstanceRepo, vast *fakeVastai, ssh *fakeSSH, eng EngineProvider) *InstanceService {
	return NewInstanceService(
		repo, vast, ssh, &fakeProbe{},
		NewModelService(&fakeModelRepo{}),
		eng,
		8000,
	)
}

// Two local rows pointing at the same vast.ai instance: exactly one must survive,
// and it must be the one holding the tunnel. The dedupe used to add the winner to
// the delete list as well, so BOTH rows were dropped — the instance vanished from
// `ps` and the surviving tunnel process was orphaned.
func TestSyncDedupeKeepsTheRowWithTheTunnel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows       []*entity.Instance
		wantPort   int
		wantKilled []int
	}{
		{
			name: "better row comes second",
			rows: []*entity.Instance{
				{VastaiID: 99, ModelName: "m", LocalPort: 0, TunnelPID: 0},
				{VastaiID: 99, ModelName: "m", LocalPort: 8000, TunnelPID: 4242},
			},
			wantPort: 8000,
		},
		{
			name: "better row comes first",
			rows: []*entity.Instance{
				{VastaiID: 99, ModelName: "m", LocalPort: 8000, TunnelPID: 4242},
				{VastaiID: 99, ModelName: "m", LocalPort: 0, TunnelPID: 0},
			},
			wantPort: 8000,
		},
		{
			name: "loser's tunnel is not leaked",
			rows: []*entity.Instance{
				{VastaiID: 99, ModelName: "m", LocalPort: 0, TunnelPID: 111},
				{VastaiID: 99, ModelName: "m", LocalPort: 8000, TunnelPID: 4242},
			},
			wantPort:   8000,
			wantKilled: []int{111},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeInstanceRepo(tc.rows...)
			vast := &fakeVastai{remote: []*RemoteInstance{
				{VastaiID: 99, ActualStatus: "running", SSHHost: "h", SSHPort: 22, HourlyRate: 0.3},
			}}
			ssh := &fakeSSH{}

			got, err := newTestInstanceSvc(repo, vast, ssh).Sync(context.Background())
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("expected exactly 1 surviving row, got %d: %+v", len(got), got)
			}
			if got[0].LocalPort != tc.wantPort {
				t.Errorf("survivor has LocalPort %d, want %d — the wrong row was kept",
					got[0].LocalPort, tc.wantPort)
			}
			// The survivor must still be reachable through the repo: the update pass
			// writes to it, and a deleted row would swallow that silently.
			if _, err := repo.FindByID(got[0].ID); err != nil {
				t.Errorf("survivor %d is not in the repo: %v", got[0].ID, err)
			}
			if got[0].HourlyRate != 0.3 {
				t.Errorf("survivor did not receive the remote update (rate %v)", got[0].HourlyRate)
			}
			for _, pid := range tc.wantKilled {
				var found bool
				for _, k := range ssh.stopped() {
					if k == pid {
						found = true
					}
				}
				if !found {
					t.Errorf("dropped row's tunnel %d was leaked; stopped=%v", pid, ssh.stopped())
				}
			}
			if len(ssh.stopped()) > len(tc.wantKilled) {
				t.Errorf("killed more tunnels than expected: %v", ssh.stopped())
			}
		})
	}
}

// A local row whose remote instance is gone must be dropped and its tunnel killed.
func TestSyncDropsRowsWhoseRemoteIsGone(t *testing.T) {
	repo := newFakeInstanceRepo(&entity.Instance{VastaiID: 7, ModelName: "m", TunnelPID: 909})
	ssh := &fakeSSH{}

	got, err := newTestInstanceSvc(repo, &fakeVastai{}, ssh).Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected the stale row to be deleted, got %+v", got)
	}
	if stopped := ssh.stopped(); len(stopped) != 1 || stopped[0] != 909 {
		t.Errorf("expected tunnel 909 to be killed, got %v", stopped)
	}
}

// vast.ai appends a status_msg, which Sync stores as "running (detail)". Budget
// used to compare with == and dropped those instances out of the totals entirely.
func TestGetBudgetCountsStatusesCarryingADetailSuffix(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	repo := newFakeInstanceRepo(
		&entity.Instance{VastaiID: 1, Status: "running", HourlyRate: 1.0, CreatedAt: created},
		&entity.Instance{VastaiID: 2, Status: "running (loading container)", HourlyRate: 2.0, CreatedAt: created},
		&entity.Instance{VastaiID: 3, Status: "stopped (user requested)", HourlyRate: 4.0, CreatedAt: created},
	)

	b, err := newTestInstanceSvc(repo, &fakeVastai{}, &fakeSSH{}).GetBudget(context.Background())
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if len(b.RunningInstances) != 2 {
		t.Errorf("expected 2 running instances, got %d — a status_msg suffix hid one", len(b.RunningInstances))
	}
	if b.TotalHourlyRate != 3.0 {
		t.Errorf("TotalHourlyRate = %v, want 3.0 (1.0 + 2.0)", b.TotalHourlyRate)
	}
	// The stopped one must contribute no hours despite its suffix.
	for _, l := range b.Lines {
		if l.Status.Is(entity.StatusStopped) && l.Cost != 0 {
			t.Errorf("stopped instance still billed %v", l.Cost)
		}
	}
}

func TestInstanceStatusIs(t *testing.T) {
	for _, tc := range []struct {
		status entity.InstanceStatus
		base   entity.InstanceStatus
		want   bool
	}{
		{"running", entity.StatusRunning, true},
		{"running (loading)", entity.StatusRunning, true},
		{"stopped", entity.StatusRunning, false},
		{"stopped (x)", entity.StatusStopped, true},
		// Must not match on a bare prefix — only on the " (detail)" form.
		{"runningish", entity.StatusRunning, false},
	} {
		if got := tc.status.Is(tc.base); got != tc.want {
			t.Errorf("(%q).Is(%q) = %v, want %v", tc.status, tc.base, got, tc.want)
		}
	}
}

// newEndpointSvc builds a service whose catalog holds exactly the given models.
func newEndpointSvc(eng EngineProvider, models ...*entity.Model) *InstanceService {
	return NewInstanceService(
		&fakeInstanceRepo{}, &fakeVastai{}, &fakeSSH{}, &fakeProbe{},
		NewModelService(&fakeModelRepo{models: models}),
		eng,
		8000,
	)
}

// The URL and the health route both belong to the engine, and both were
// hardcoded to llama.cpp's by every caller. `ps` printed
// http://localhost:8000/v1 for a ComfyUI instance — a 404 — and probed
// /v1/models, which ComfyUI does not have, so a perfectly healthy image
// generator was reported unhealthy for the life of the rental. Same shape of
// defect as the tunnel that forwarded to port 8000 whatever the engine.
func TestEndpointsFollowTheEngineNotLlamaCpp(t *testing.T) {
	for _, tc := range []struct {
		name             string
		model            *entity.Model
		enginePath       string
		wantURL          string
		wantProbe        string
		wantExpectModels bool
	}{
		{
			name:             "llama.cpp serves the OpenAI API under /v1",
			model:            &entity.Model{Name: "coder", EngineType: entity.EngineLlamaCpp},
			enginePath:       "/v1/models",
			wantURL:          "http://localhost:8000/v1",
			wantProbe:        "http://localhost:8000/v1/models",
			wantExpectModels: true,
		},
		{
			// Rows written before the engine split carry no EngineType at all.
			name:             "an unset engine type is llama.cpp",
			model:            &entity.Model{Name: "coder"},
			enginePath:       "/v1/models",
			wantURL:          "http://localhost:8000/v1",
			wantProbe:        "http://localhost:8000/v1/models",
			wantExpectModels: true,
		},
		{
			name:             "ComfyUI serves a UI at the root",
			model:            &entity.Model{Name: "comfyui", EngineType: entity.EngineComfyUI},
			enginePath:       "/history",
			wantURL:          "http://localhost:8000",
			wantProbe:        "http://localhost:8000/history",
			wantExpectModels: false,
		},
		{
			name:             "Jupyter serves a UI at the root",
			model:            &entity.Model{Name: "jupyter-pytorch", EngineType: entity.EngineJupyter},
			enginePath:       "/",
			wantURL:          "http://localhost:8000",
			wantProbe:        "http://localhost:8000/",
			wantExpectModels: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newEndpointSvc(&fakeEngine{healthPath: tc.enginePath}, tc.model)
			inst := &entity.Instance{ModelName: tc.model.Name, LocalPort: 8000}

			if got := svc.TunnelURL(inst); got != tc.wantURL {
				t.Errorf("TunnelURL = %q, want %q", got, tc.wantURL)
			}
			gotProbe, gotExpect := svc.HealthProbe(inst)
			if gotProbe != tc.wantProbe {
				t.Errorf("HealthProbe url = %q, want %q", gotProbe, tc.wantProbe)
			}
			if gotExpect != tc.wantExpectModels {
				t.Errorf("HealthProbe expectModelList = %v, want %v", gotExpect, tc.wantExpectModels)
			}
		})
	}
}

// An instance whose catalog entry has been renamed or removed must still be
// reachable — the same fallback remotePort makes, for the same reason.
func TestEndpointsFallBackToLlamaCppForAnUnknownModel(t *testing.T) {
	svc := newEndpointSvc(&fakeEngine{})
	inst := &entity.Instance{ModelName: "deleted-from-the-catalog", LocalPort: 8000}

	if got := svc.TunnelURL(inst); got != "http://localhost:8000/v1" {
		t.Errorf("TunnelURL = %q, want the llama.cpp fallback", got)
	}
	if _, expectModels := svc.HealthProbe(inst); !expectModels {
		t.Error("an unknown model should be probed as llama.cpp")
	}
}

// No tunnel means nothing to show and nothing to probe, and `ps` renders that as
// a dash rather than as a URL nobody can open.
func TestEndpointsAreEmptyWithoutATunnel(t *testing.T) {
	svc := newEndpointSvc(&fakeEngine{}, &entity.Model{Name: "coder"})
	inst := &entity.Instance{ModelName: "coder", LocalPort: 0}

	if got := svc.TunnelURL(inst); got != "" {
		t.Errorf("TunnelURL = %q for an instance with no tunnel, want empty", got)
	}
	if url, _ := svc.HealthProbe(inst); url != "" {
		t.Errorf("HealthProbe url = %q for an instance with no tunnel, want empty", url)
	}
}
