package ssh

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type Tunnel struct {
	LocalPort int
	SSHHost   string
	SSHPort   int
	PID       int
}

// StartTunnel creates an SSH tunnel forwarding localPort to remote localhost:8000.
func StartTunnel(localPort int, sshHost string, sshPort int) (*Tunnel, error) {
	// Find SSH key
	homeDir, _ := os.UserHomeDir()
	sshKey := ""
	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		path := filepath.Join(homeDir, ".ssh", name)
		if _, err := os.Stat(path); err == nil {
			sshKey = path
			break
		}
	}

	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "ServerAliveInterval=30",
		"-o", "ExitOnForwardFailure=yes",
		"-N",
		"-L", fmt.Sprintf("%d:localhost:8000", localPort),
		"-p", fmt.Sprintf("%d", sshPort),
	}
	if sshKey != "" {
		args = append(args, "-i", sshKey)
	}
	args = append(args, fmt.Sprintf("root@%s", sshHost))

	cmd := exec.Command("ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	fmt.Printf("[ssh] %s\n", cmd.String())

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh tunnel: %w", err)
	}

	return &Tunnel{
		LocalPort: localPort,
		SSHHost:   sshHost,
		SSHPort:   sshPort,
		PID:       cmd.Process.Pid,
	}, nil
}

// StopTunnel kills the SSH tunnel process by PID.
func StopTunnel(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// WaitForSSH waits until SSH is reachable on the given host:port, respecting context deadline.
func WaitForSSH(ctx context.Context, host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("SSH not reachable at %s: %w", addr, ctx.Err())
		case <-ticker.C:
		}
	}
}

// WaitForVLLMHealth waits until the model server responds via the local tunnel, respecting context deadline.
func WaitForVLLMHealth(ctx context.Context, localPort int) error {
	url := fmt.Sprintf("http://localhost:%d/v1/models", localPort)
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("vLLM not healthy at port %d: %w", localPort, ctx.Err())
		case <-ticker.C:
		}
	}
}

// RunRemoteCommand executes a command on the remote instance via SSH.
func RunRemoteCommand(sshHost string, sshPort int, command string) ([]byte, error) {
	cmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=10",
		"-p", fmt.Sprintf("%d", sshPort),
		fmt.Sprintf("root@%s", sshHost),
		command,
	)
	fmt.Printf("[ssh] %s\n", cmd.String())
	return cmd.CombinedOutput()
}

// FindFreePort returns an available TCP port starting from basePort.
func FindFreePort(basePort int) (int, error) {
	for port := basePort; port < basePort+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port found starting from %d", basePort)
}
