package ssh

import (
	"bytes"
	"context"
	"errors"
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

// sshRetryInterval is the gap between authentication probes in WaitForSSH.
var sshRetryInterval = 5 * time.Second

// sshHeartbeatInterval is how often WaitForSSH reports that it is still trying,
// and why.
//
// "Waiting for SSH..." used to be the last thing printed before a deploy went
// silent for as long as its deadline allowed — 28 minutes, in the case that
// prompted this. The download progress reporter cannot cover that stretch: it
// runs over SSH, so it only starts once the tunnel is up. Echoing ssh's own last
// line turns the silence into a diagnosis, since "Permission denied (publickey)"
// and "Connection refused" call for completely different responses.
//
// Every 30s rather than on each 5s probe: often enough to prove the deploy is
// alive, rare enough not to bury the surrounding output.
var sshHeartbeatInterval = 30 * time.Second

// authGracePeriod is how long a host may go on refusing our key before
// WaitForSSH calls the refusal final.
//
// vast.ai installs authorized_keys while the container comes up, so a key
// refused at first has usually just been asked about early — that race is what
// the retry loop exists for. A key still refused three minutes after sshd
// started answering is not that race, and spending a 30-minute deploy deadline
// to reach the same conclusion costs a GPU-hour of nothing.
//
// Three minutes, not the 90s this started at, for the same reason the startup
// timeouts are generous (see "Where the timeout numbers come from"): the two
// errors are not symmetric. Waiting 90s too long costs 90s. Giving up 90s too
// early destroys a working deploy *and* blacklists a machine that was merely
// slow — and nothing here measures how long after `actual_status: running`
// vast.ai actually finishes registering the key, so the margin is a guess and
// should be a generous one. Still ~10x faster than the deadline it replaces.
//
// It also has to stay above the 2-minute budget InstanceService.EstablishTunnel
// wraps around WaitForSSH, or `mycodeagent tunnel` would inherit a deploy-shaped
// verdict it never asked for.
var authGracePeriod = 3 * time.Minute

// ErrKeyRejected marks the terminal "this host will never accept our key"
// verdict, so callers can act on the diagnosis rather than substring-matching a
// message. Mirrors how DeployService already tests for context.Canceled.
var ErrKeyRejected = errors.New("SSH key rejected")

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
//
// The other two options exist to remove permanent ssh failures that the retry
// loop cannot tell from a transient, and so would re-run to the deadline.
func baseArgs(sshPort int) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		// vast.ai hands out ssh1.vast.ai:PORT from a pool, so a port seen before
		// comes back attached to a different instance with a different host key.
		// StrictHostKeyChecking=no accepts an *unknown* host but still hard-fails
		// a *changed* one ("REMOTE HOST IDENTIFICATION HAS CHANGED"), which no
		// amount of retrying clears. None of these boxes outlives a session, so
		// there is nothing worth remembering between them.
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-p", strconv.Itoa(sshPort),
	}
	if key := identityFile(); key != "" {
		// IdentitiesOnly stops ssh offering every key an agent happens to hold
		// before ours: past six the server disconnects with "Too many
		// authentication failures" and our key is never tried at all.
		// Conditional, because with no -i to fall back on it would leave ssh
		// with nothing to offer.
		args = append(args, "-i", key, "-o", "IdentitiesOnly=yes")
	}
	return args
}

// defaultRemotePort is the forward target when a caller supplies none. It is
// llama.cpp's port, which every caller used implicitly while llama.cpp was the
// only engine.
const defaultRemotePort = 8000

// StartTunnel creates an SSH tunnel forwarding localPort to remotePort on the
// instance, and returns only once the forward is actually accepting connections.
//
// remotePort is a parameter, not a constant, because the engine decides it:
// llama-server listens on 8000, ComfyUI on 8188, Jupyter on 8888. It was
// hardcoded to 8000 here, so every non-llama.cpp engine got a tunnel pointed at
// a port nothing was listening on — the deploy then health-checked through it
// until the startup deadline and destroyed a perfectly good instance.
//
// The readiness check is the other half. This used to Start() the process and
// report success immediately, so an ssh that died on the spot — rejected key,
// refused forward — still looked like a working tunnel. The deploy then polled
// localhost through nothing at all until it timed out, and the dead ssh sat
// around as a zombie because nobody ever reaped it. Observed live: a host whose
// TCP port answered but whose key auth failed burned a full deploy that way.
func StartTunnel(localPort int, sshHost string, sshPort, remotePort int) (*Tunnel, error) {
	if remotePort <= 0 {
		remotePort = defaultRemotePort
	}
	args := append(baseArgs(sshPort),
		"-o", "ServerAliveInterval=30",
		"-o", "ExitOnForwardFailure=yes",
		"-N",
		"-L", fmt.Sprintf("%d:localhost:%d", localPort, remotePort),
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
// Retrying preserves the tolerant behaviour for the normal case, where
// authorized_keys simply takes a few seconds to land — but only up to
// authGracePeriod once the host starts actively rejecting the key. See
// keyRejected for why that one failure is worth singling out.
func WaitForSSH(ctx context.Context, host string, port int) error {
	// JoinHostPort, not Sprintf("%s:%d"): the latter produces an unusable address
	// for an IPv6 literal (go vet flags it).
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	return waitForSSH(ctx, addr, func(ctx context.Context) ([]byte, error) {
		return runRemoteCommand(ctx, host, port, "true")
	}, time.Now)
}

// waitForSSH is the retry loop, split from WaitForSSH so tests can drive it with
// a stub probe and a stub clock. A host that completes a handshake and *then*
// refuses the key cannot be simulated with a bare TCP listener, which is all the
// other tests here have to work with, and a grace period measured against the
// wall clock cannot be tested at its boundary without one.
func waitForSSH(ctx context.Context, addr string, probe func(context.Context) ([]byte, error), now func() time.Time) error {
	ticker := time.NewTicker(sshRetryInterval)
	defer ticker.Stop()

	// Zero until the host first refuses the key. The clock that matters starts
	// when sshd is answering, not when the deploy began — a minute of
	// "connection refused" while the container boots says nothing about a key
	// nobody has been asked about yet, and must not eat the grace the key is
	// owed.
	//
	// Deliberately never reset once set. Requiring an *uninterrupted* run of
	// rejections looked stricter, but vast.ai's shared ssh proxy drops the
	// occasional connection, and a single "Connection closed by remote host"
	// inside the window would restart the clock — reinstating the full-deadline
	// burn on exactly the flaky hosts this exists to catch.
	var refusingSince time.Time
	// startedAt doubles as the heartbeat baseline. Both are set on the first
	// failed probe rather than before the loop, so the clock is read exactly once
	// per attempt.
	var startedAt, lastBeat time.Time

	var lastErr error
	for {
		out, err := probe(ctx)
		if err == nil {
			return nil
		}
		reason := strings.TrimSpace(lastLine(string(out)))
		lastErr = fmt.Errorf("%v: %s", err, reason)

		at := now()
		if startedAt.IsZero() {
			startedAt, lastBeat = at, at
		} else if at.Sub(lastBeat) >= sshHeartbeatInterval {
			fmt.Printf("  still waiting for SSH after %s: %s\n", at.Sub(startedAt).Round(time.Second), reason)
			lastBeat = at
		}

		if keyRejected(reason) {
			if refusingSince.IsZero() {
				refusingSince = at
			}
			if refused := at.Sub(refusingSince); refused >= authGracePeriod {
				return fmt.Errorf(
					"%w at %s after %s: %s — sshd is up and refusing our key, which waiting "+
						"cannot fix. Usually a host-side ownership or mode problem on the "+
						"instance's /root/.ssh/authorized_keys. If the next deploy fails the "+
						"same way on a *different* machine, suspect the local key instead and "+
						"re-run `mycodeagent login` — this cannot tell the two apart",
					ErrKeyRejected, addr, refused.Round(time.Second), reason)
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("SSH not usable at %s: %w (last attempt: %v)", addr, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

// keyRejected reports whether ssh's own last line says the server completed a
// handshake and turned our public key down.
//
// Finality is inferred from the rejection persisting rather than read off the
// cause, because the cause never reaches us: sshd's actual reason — here
// "Authentication refused: bad ownership or modes for file
// /root/.ssh/authorized_keys" — goes to the *server's* log, retrievable only
// through the vast.ai logs API, and is never sent to the client.
//
// This is not the only permanent ssh failure that exists, just the only one left
// that has to be inferred. The other two reachable here — a changed host key and
// "Too many authentication failures" — are prevented outright in baseArgs, which
// is the better fix where it is available.
//
// Matched on the two parts rather than a fixed string: the parenthesised list
// is whatever the server offered ("publickey", "publickey,password", …).
func keyRejected(sshOutput string) bool {
	return strings.Contains(sshOutput, "Permission denied") &&
		strings.Contains(sshOutput, "publickey")
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
// healthPath is the HTTP path to check (e.g., "/v1/models", "/history", "/").
func WaitForServerHealth(ctx context.Context, localPort int, healthPath string) error {
	if healthPath == "skip" {
		return nil
	}
	if healthPath == "" {
		healthPath = "/v1/models"
	}
	url := fmt.Sprintf("http://localhost:%d%s", localPort, healthPath)
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
	return runRemoteCommand(context.Background(), sshHost, sshPort, command)
}

// runRemoteCommand is RunRemoteCommand with a caller-supplied context, so a
// probe already in flight is abandoned the moment the deploy is cancelled.
//
// The timeout is still applied on top. ConnectTimeout only bounds the TCP
// connect, so a peer that accepts and then never completes the handshake leaves
// ssh waiting indefinitely; bounding the invocation is what stops WaitForSSH
// overrunning its own deadline, since it can only test the context between
// attempts. Honouring the parent as well shrinks "up to 30s late" to "at once".
func runRemoteCommand(ctx context.Context, sshHost string, sshPort int, command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteCommandTimeout)
	defer cancel()

	args := append(baseArgs(sshPort), fmt.Sprintf("root@%s", sshHost), command)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	// Distinguish the two, or a cancelled deploy reports itself as a timeout —
	// and DeployService blames hosts for timeouts but never for cancellation.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return out, fmt.Errorf("ssh timed out after %s: %w", remoteCommandTimeout, ctxErr)
		}
		return out, fmt.Errorf("ssh cancelled: %w", ctxErr)
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
