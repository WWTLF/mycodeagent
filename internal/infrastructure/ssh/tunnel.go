package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Tunnel struct {
	LocalPort int
	SSHHost   string
	SSHPort   int
	PID       int
}

// tunnelReadyTimeout is how long StartTunnel waits for the forward to come up
// before giving up. Generous enough for a slow handshake, short enough that a
// dead tunnel is reported in seconds rather than discovered by a health check
// that polls a closed port until the deploy's deadline.
const tunnelReadyTimeout = 25 * time.Second

// remoteCommandTimeout bounds a single ssh invocation. Comfortably above the
// restart sweep (which sleeps ~4s) and the liveness probe, well below any
// deploy deadline.
var remoteCommandTimeout = 30 * time.Second

// identityFile returns the first usable private key, or "" to let ssh decide.
func identityFile() string {
	homeDir, _ := os.UserHomeDir()
	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		path := filepath.Join(homeDir, ".ssh", name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// baseArgs are the options every ssh invocation shares. They live in one place
// so the auth probe, the tunnel and remote commands cannot drift apart — a probe
// that authenticates differently from the tunnel proves nothing about it.
//
// BatchMode=yes matters: without it a host that rejects our key can leave ssh
// sitting on a password prompt instead of failing.
func baseArgs(sshPort int) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-p", strconv.Itoa(sshPort),
	}
	if key := identityFile(); key != "" {
		args = append(args, "-i", key)
	}
	return args
}

// StartTunnel creates an SSH tunnel forwarding localPort to remote localhost:8000
// and returns only once the forward is actually accepting connections.
//
// The verification is the point. This used to Start() the process and report
// success immediately, so an ssh that died on the spot — rejected key, refused
// forward — still looked like a working tunnel. The deploy then polled
// localhost through nothing at all until it timed out, and the dead ssh sat
// around as a zombie because nobody ever reaped it. Observed live: a host whose
// TCP port answered but whose key auth failed burned a full deploy that way.
func StartTunnel(localPort int, sshHost string, sshPort int) (*Tunnel, error) {
	args := append(baseArgs(sshPort),
		"-o", "ServerAliveInterval=30",
		"-o", "ExitOnForwardFailure=yes",
		"-N",
		"-L", fmt.Sprintf("%d:localhost:8000", localPort),
		fmt.Sprintf("root@%s", sshHost),
	)

	cmd := exec.Command("ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // survive Ctrl-C on the CLI
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	fmt.Printf("[ssh] %s\n", cmd.String())

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh tunnel: %w", err)
	}

	// Reaps the child so a tunnel that dies does not linger as a zombie, and
	// tells us immediately when it does.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	deadline := time.After(tunnelReadyTimeout)
	for {
		select {
		case waitErr := <-exited:
			return nil, fmt.Errorf("ssh tunnel exited immediately (%v): %s",
				waitErr, strings.TrimSpace(stderr.String()))
		case <-deadline:
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("ssh tunnel did not accept connections on %s within %s: %s",
				addr, tunnelReadyTimeout, strings.TrimSpace(stderr.String()))
		default:
		}
		// Connecting proves ssh bound the local port and is forwarding. Whether
		// the *remote* side is serving yet is the health check's job.
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			conn.Close()
			return &Tunnel{
				LocalPort: localPort,
				SSHHost:   sshHost,
				SSHPort:   sshPort,
				PID:       cmd.Process.Pid,
			}, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// StopTunnel kills the SSH tunnel process by PID.
func StopTunnel(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// WaitForSSH waits until SSH is usable — reachable AND willing to authenticate —
// on the given host:port, respecting the context deadline.
//
// It runs a real command rather than dialing the port, because vast.ai answers
// on the SSH port long before the instance accepts our key, and sometimes never
// accepts it at all: a host was seen answering TCP on its forwarded port while
// every key exchange came back "Permission denied (publickey)". A TCP-only check
// passed that host, the deploy went on to open a tunnel that died on the spot,
// and the whole startup budget was spent polling a port with nothing behind it.
//
// Retrying to the deadline preserves the tolerant behaviour for the normal case,
// where authorized_keys simply takes a few seconds to land.
func WaitForSSH(ctx context.Context, host string, port int) error {
	// JoinHostPort, not Sprintf("%s:%d"): the latter produces an unusable address
	// for an IPv6 literal (go vet flags it).
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		out, err := RunRemoteCommand(host, port, "true")
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%v: %s", err, strings.TrimSpace(lastLine(string(out))))

		select {
		case <-ctx.Done():
			return fmt.Errorf("SSH not usable at %s: %w (last attempt: %v)", addr, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

// lastLine returns the final non-empty line, which for ssh is the actual reason
// ("Permission denied (publickey)") rather than the host's welcome banner.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// WaitForServerHealth waits until the model server responds via the local tunnel,
// respecting the context deadline. llama-server binds the port early and answers
// 503 on every route except /health until the model is fully loaded, so a 200 on
// /v1/models means "weights loaded and serving", not just "process up".
func WaitForServerHealth(ctx context.Context, localPort int) error {
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
			return fmt.Errorf("model server not healthy at port %d: %w", localPort, ctx.Err())
		case <-ticker.C:
		}
	}
}

// RunRemoteCommand executes a command on the remote instance via SSH.
//
// Shares baseArgs with StartTunnel so it authenticates exactly the same way —
// it doubles as the reachability probe for WaitForSSH, and a probe that used a
// different key or different options would not prove the tunnel can connect.
//
// Deliberately silent: the liveness watcher calls this every 20s for the whole
// startup, and echoing the command there buried the deploy progress output.
// Callers that want the user to see the step print their own line.
func RunRemoteCommand(sshHost string, sshPort int, command string) ([]byte, error) {
	// Hard cap on every invocation. ConnectTimeout only bounds the TCP connect,
	// so a peer that accepts the connection and then never completes the
	// handshake leaves ssh waiting indefinitely — which made WaitForSSH overrun
	// its own deadline, since it can only check the context between attempts.
	ctx, cancel := context.WithTimeout(context.Background(), remoteCommandTimeout)
	defer cancel()

	args := append(baseArgs(sshPort), fmt.Sprintf("root@%s", sshHost), command)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("ssh timed out after %s: %w", remoteCommandTimeout, ctx.Err())
	}
	return out, err
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
