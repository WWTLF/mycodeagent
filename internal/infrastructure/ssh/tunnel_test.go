package ssh

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func requireSSH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh binary not available")
	}
}

// closedPort returns a port nothing is listening on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// The regression this guards: StartTunnel used to Start() the ssh process and
// report success immediately. An ssh that died on the spot — rejected key,
// refused forward, unreachable host — still returned a PID, so the deploy went
// on to poll a local port with nothing behind it until its deadline, and the
// dead process lingered as a zombie because nobody reaped it.
func TestStartTunnelFailsWhenSSHCannotConnect(t *testing.T) {
	requireSSH(t)

	local := closedPort(t)
	start := time.Now()
	tun, err := StartTunnel(local, "127.0.0.1", closedPort(t))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("StartTunnel reported success for a tunnel that cannot exist: %+v", tun)
	}
	if tun != nil {
		t.Errorf("expected no Tunnel alongside the error, got %+v", tun)
	}
	// It must fail from the process exiting, not by burning the whole timeout.
	if elapsed > tunnelReadyTimeout {
		t.Errorf("took %s — should have failed as soon as ssh exited", elapsed)
	}
	// And the error should carry ssh's own explanation, not a bare "failed".
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Errorf("error does not identify the cause: %v", err)
	}

	// Nothing may be left bound on the local port.
	if conn, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), 300*time.Millisecond); derr == nil {
		conn.Close()
		t.Errorf("local port %d is still bound after a failed tunnel", local)
	}
}

// WaitForSSH must verify it can actually log in. It used to dial the TCP port
// and call that success, which let a deploy proceed to a host that answered on
// the port but refused every key.
func TestWaitForSSHRequiresAuthNotJustAnOpenPort(t *testing.T) {
	requireSSH(t)

	// A listener that accepts connections and says nothing: TCP is up, SSH is
	// not. The old implementation returned nil here in milliseconds.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold it open without speaking the protocol.
			go func() { defer c.Close(); time.Sleep(30 * time.Second) }()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	// Shrink the per-invocation cap: the point is that WaitForSSH honours its
	// deadline even when ssh hangs, and the real 30s cap would make this slow.
	restore := remoteCommandTimeout
	remoteCommandTimeout = 2 * time.Second
	defer func() { remoteCommandTimeout = restore }()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	start := time.Now()
	err = WaitForSSH(ctx, "127.0.0.1", port)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("WaitForSSH accepted a port that never completes an SSH handshake")
	}
	if !strings.Contains(err.Error(), "not usable") {
		t.Errorf("unexpected error shape: %v", err)
	}
	// A hung ssh must not drag WaitForSSH past its deadline.
	if elapsed > 8*time.Second {
		t.Errorf("overran its 4s deadline by too much: %s", elapsed)
	}
}

// Every ssh entry point must authenticate identically, or the reachability probe
// proves nothing about the tunnel that follows it.
func TestBaseArgsSharedByProbeAndTunnel(t *testing.T) {
	args := strings.Join(baseArgs(2222), " ")

	for _, want := range []string{
		"StrictHostKeyChecking=no",
		// Without BatchMode a host that rejects the key can leave ssh waiting on
		// a password prompt instead of failing.
		"BatchMode=yes",
		"ConnectTimeout=10",
		"-p 2222",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("baseArgs missing %q: %s", want, args)
		}
	}
	if key := identityFile(); key != "" && !strings.Contains(args, "-i "+key) {
		t.Errorf("baseArgs does not pass the identity file %q: %s", key, args)
	}
}
