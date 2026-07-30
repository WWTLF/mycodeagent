package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// In-memory doubles for every provider/repository the services depend on. They
// exist because the interesting failure modes here are cleanup and reconciliation
// paths — the ones that only ever run when something has already gone wrong, and
// so never get exercised by hand.

// ---------------------------------------------------------------- instance repo

type fakeInstanceRepo struct {
	mu     sync.Mutex
	rows   map[int64]*entity.Instance
	nextID int64
	order  []int64 // preserves insertion order so FindAll is deterministic

	saveErr error
}

var _ repository.InstanceRepository = (*fakeInstanceRepo)(nil)

func newFakeInstanceRepo(seed ...*entity.Instance) *fakeInstanceRepo {
	r := &fakeInstanceRepo{rows: map[int64]*entity.Instance{}}
	for _, s := range seed {
		cp := *s
		if cp.ID == 0 {
			r.nextID++
			cp.ID = r.nextID
		} else if cp.ID > r.nextID {
			r.nextID = cp.ID
		}
		r.rows[cp.ID] = &cp
		r.order = append(r.order, cp.ID)
	}
	return r
}

func (r *fakeInstanceRepo) Save(inst *entity.Instance) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	inst.ID = r.nextID
	cp := *inst
	r.rows[cp.ID] = &cp
	r.order = append(r.order, cp.ID)
	return nil
}

func (r *fakeInstanceRepo) FindByID(id int64) (*entity.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if inst, ok := r.rows[id]; ok {
		cp := *inst
		return &cp, nil
	}
	return nil, fmt.Errorf("instance not found")
}

func (r *fakeInstanceRepo) FindByVastaiID(vastaiID int64) (*entity.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		if inst, ok := r.rows[id]; ok && inst.VastaiID == vastaiID {
			cp := *inst
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("instance not found")
}

func (r *fakeInstanceRepo) FindAll() ([]*entity.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entity.Instance
	for _, id := range r.order {
		if inst, ok := r.rows[id]; ok {
			cp := *inst
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *fakeInstanceRepo) FindRunning() ([]*entity.Instance, error) {
	all, _ := r.FindAll()
	var out []*entity.Instance
	for _, i := range all {
		if i.Status.Is(entity.StatusRunning) || i.Status.Is(entity.StatusStarting) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (r *fakeInstanceRepo) Update(inst *entity.Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[inst.ID]; !ok {
		// Mirrors SQLite: UPDATE on a missing row is a silent no-op. Tests assert
		// on the resulting row set, so a lost update shows up as missing data.
		return nil
	}
	cp := *inst
	r.rows[inst.ID] = &cp
	return nil
}

func (r *fakeInstanceRepo) Delete(id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}

func (r *fakeInstanceRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *fakeInstanceRepo) ids() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int64
	for _, id := range r.order {
		if _, ok := r.rows[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// ------------------------------------------------------------------ model repo

type fakeModelRepo struct{ models []*entity.Model }

var _ repository.ModelRepository = (*fakeModelRepo)(nil)

func (r *fakeModelRepo) FindByName(name string) (*entity.Model, error) {
	for _, m := range r.models {
		if m.Name == name || m.Alias == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", name)
}
func (r *fakeModelRepo) FindByAlias(a string) (*entity.Model, error) { return r.FindByName(a) }
func (r *fakeModelRepo) FindAll() ([]*entity.Model, error)           { return r.models, nil }
func (r *fakeModelRepo) FindByCategory(c entity.ModelCategory) ([]*entity.Model, error) {
	return r.models, nil
}

// --------------------------------------------------------------- bad host repo

type fakeBadHostRepo struct {
	added map[int]string
	list  []*entity.BadHost
}

var _ repository.BadHostRepository = (*fakeBadHostRepo)(nil)

func newFakeBadHostRepo() *fakeBadHostRepo {
	return &fakeBadHostRepo{added: map[int]string{}}
}
func (r *fakeBadHostRepo) Add(machineID int, reason string) error {
	r.added[machineID] = reason
	return nil
}
func (r *fakeBadHostRepo) List() ([]*entity.BadHost, error) { return r.list, nil }
func (r *fakeBadHostRepo) IsBad(machineID int) (bool, error) {
	_, ok := r.added[machineID]
	return ok, nil
}
func (r *fakeBadHostRepo) Delete(machineID int) error { delete(r.added, machineID); return nil }
func (r *fakeBadHostRepo) Clear() error               { r.added = map[int]string{}; return nil }

// ------------------------------------------------------------------- vast.ai

type fakeVastai struct {
	offers []OfferResult
	remote []*RemoteInstance

	createdID int
	createErr error
	waitErr   error

	destroyed  []int
	destroyErr error
	stopped    []int
	started    []int
	startErr   error
}

var _ VastaiProvider = (*fakeVastai)(nil)

func (v *fakeVastai) SearchOffers(minGPURAM, numGPUs, minDiskGB int) ([]OfferResult, error) {
	return v.offers, nil
}
func (v *fakeVastai) CreateInstance(offerID int, image string, env map[string]string, onstart string, diskGB int) (int, error) {
	if v.createErr != nil {
		return 0, v.createErr
	}
	return v.createdID, nil
}
func (v *fakeVastai) WaitForInstance(ctx context.Context, id int) (string, int, float64, error) {
	if v.waitErr != nil {
		return "", 0, 0, v.waitErr
	}
	return "ssh.example", 2222, 0.25, nil
}
func (v *fakeVastai) StopInstance(id int) error { v.stopped = append(v.stopped, id); return nil }
func (v *fakeVastai) StartInstance(id int) error {
	v.started = append(v.started, id)
	return v.startErr
}
func (v *fakeVastai) DestroyInstance(id int) error {
	v.destroyed = append(v.destroyed, id)
	return v.destroyErr
}
func (v *fakeVastai) GetInstance(ctx context.Context, id int) (*RemoteInstance, error) {
	for _, r := range v.remote {
		if r.VastaiID == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("no such remote instance %d", id)
}
func (v *fakeVastai) ListRemoteInstances(ctx context.Context) ([]*RemoteInstance, error) {
	return v.remote, nil
}
func (v *fakeVastai) GetInstanceLogs(ctx context.Context, id int, tail string) ([]byte, error) {
	return nil, nil
}
func (v *fakeVastai) VerifyAPIKey(ctx context.Context, k string) error { return nil }
func (v *fakeVastai) ListSSHKeys(ctx context.Context, k string) ([]string, error) {
	return nil, nil
}
func (v *fakeVastai) CreateSSHKey(ctx context.Context, k, pub string) error { return nil }

// ----------------------------------------------------------------------- ssh

type fakeSSH struct {
	mu sync.Mutex

	tunnelPID  int
	startErr   error
	healthErr  error
	healthWait time.Duration // block this long before returning healthErr

	stoppedPIDs []int
	remoteOut   map[string]string // command substring → output
}

var _ SSHTunnelProvider = (*fakeSSH)(nil)

func (s *fakeSSH) StartTunnel(localPort int, host string, port int) (int, error) {
	if s.startErr != nil {
		return 0, s.startErr
	}
	return s.tunnelPID, nil
}
func (s *fakeSSH) StopTunnel(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stoppedPIDs = append(s.stoppedPIDs, pid)
	return nil
}
func (s *fakeSSH) WaitForSSH(ctx context.Context, host string, port int) error { return nil }
func (s *fakeSSH) RunRemoteCommand(host string, port int, cmd string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for frag, out := range s.remoteOut {
		if strings.Contains(cmd, frag) {
			return []byte(out), nil
		}
	}
	return []byte("ALIVE"), nil
}
func (s *fakeSSH) FindFreePort(basePort int) (int, error) { return basePort, nil }
func (s *fakeSSH) WaitForServerHealth(ctx context.Context, localPort int) error {
	if s.healthWait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.healthWait):
		}
	}
	return s.healthErr
}

func (s *fakeSSH) stopped() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.stoppedPIDs...)
}

// -------------------------------------------------------------------- engine

type fakeEngine struct {
	lastNumGPUs, lastContext int
}

var _ EngineProvider = (*fakeEngine)(nil)

func (e *fakeEngine) DockerImage() string { return "test/image:1" }
func (e *fakeEngine) BuildOnstart(m *entity.Model, numGPUs, ctxLen int, hfToken string) string {
	e.lastNumGPUs, e.lastContext = numGPUs, ctxLen
	return fmt.Sprintf("echo 'script ctx=%d gpus=%d' > /tmp/s.sh && bash /tmp/s.sh", ctxLen, numGPUs)
}
func (e *fakeEngine) BuildRawCommand(m *entity.Model, numGPUs, ctxLen int) string { return "run" }
func (e *fakeEngine) RestartCommands(m *entity.Model) (string, string)            { return "kill", "start" }
func (e *fakeEngine) LivenessCommand() string                                     { return "liveness" }
func (e *fakeEngine) LogPath() string                                             { return "/tmp/llama.log" }

// --------------------------------------------------------------------- probe

type fakeProbe struct {
	id     string
	maxLen int
}

var _ ServerProbe = (*fakeProbe)(nil)

func (p *fakeProbe) GetServedModel(ctx context.Context, localPort int) (string, int, error) {
	return p.id, p.maxLen, nil
}
