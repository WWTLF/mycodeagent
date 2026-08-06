package ssh

import (
	"context"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"

	"github.com/WWTLF/mycodeagent/internal/domain/service"
)

// Adapter wraps SSH functions to implement service.SSHTunnelProvider.
type Adapter struct{}

var _ service.SSHTunnelProvider = (*Adapter)(nil)

func NewAdapter() *Adapter {
	return &Adapter{}
}

func (a *Adapter) StartTunnel(localPort int, sshHost string, sshPort, remotePort int, release func()) (int, error) {
	t, err := StartTunnel(localPort, sshHost, sshPort, remotePort, release)
	if err != nil {
		return 0, err
	}
	return t.PID, nil
}

func (a *Adapter) StopTunnel(pid int) error {
	return StopTunnel(pid)
}

func (a *Adapter) StartSync(sshHost string, sshPort int, dirs []entity.SyncDir, workDir string) (int, string, error) {
	return StartSync(sshHost, sshPort, dirs, workDir)
}

func (a *Adapter) StopSync(pid int) error {
	return StopSync(pid)
}

func (a *Adapter) WaitForSSH(ctx context.Context, host string, port int) error {
	return WaitForSSH(ctx, host, port)
}

func (a *Adapter) RunRemoteCommand(sshHost string, sshPort int, command string) ([]byte, error) {
	return RunRemoteCommand(sshHost, sshPort, command)
}

func (a *Adapter) ReservePort(preferred, basePort int) (int, func(), error) {
	return ReservePort(preferred, basePort)
}

func (a *Adapter) WaitForServerHealth(ctx context.Context, localPort int, healthPath string) error {
	return WaitForServerHealth(ctx, localPort, healthPath)
}
