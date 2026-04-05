package ssh

import (
	"github.com/WWTLF/mycodeagent/internal/domain/service"
)

// Adapter wraps SSH functions to implement service.SSHTunnelProvider.
type Adapter struct{}

var _ service.SSHTunnelProvider = (*Adapter)(nil)

func NewAdapter() *Adapter {
	return &Adapter{}
}

func (a *Adapter) StartTunnel(localPort int, sshHost string, sshPort int) (int, error) {
	t, err := StartTunnel(localPort, sshHost, sshPort)
	if err != nil {
		return 0, err
	}
	return t.PID, nil
}

func (a *Adapter) StopTunnel(pid int) error {
	return StopTunnel(pid)
}

func (a *Adapter) WaitForSSH(host string, port int) error {
	return WaitForSSH(host, port, 24)
}

func (a *Adapter) RunRemoteCommand(sshHost string, sshPort int, command string) ([]byte, error) {
	return RunRemoteCommand(sshHost, sshPort, command)
}

func (a *Adapter) FindFreePort(basePort int) (int, error) {
	return FindFreePort(basePort)
}

func (a *Adapter) WaitForVLLMHealth(localPort int) error {
	return WaitForVLLMHealth(localPort, 60)
}
