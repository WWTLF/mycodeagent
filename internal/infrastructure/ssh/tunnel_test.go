package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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
	tun, err := StartTunnel(local, "127.0.0.1", closedPort(t), 8000, nil)
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
	shrinkRemoteCommandTimeout(t, 2*time.Second)

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

// shrinkSSHTiming scales the retry loop down to test speed and restores it after.
func shrinkSSHTiming(t *testing.T, grace, interval time.Duration) {
	t.Helper()
	oldGrace, oldInterval := authGracePeriod, sshRetryInterval
	authGracePeriod, sshRetryInterval = grace, interval
	t.Cleanup(func() { authGracePeriod, sshRetryInterval = oldGrace, oldInterval })
}

// shrinkRemoteCommandTimeout caps a single ssh invocation, for the tests that
// shell out to a real ssh. Separate knob, same t.Cleanup idiom.
func shrinkRemoteCommandTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := remoteCommandTimeout
	remoteCommandTimeout = d
	t.Cleanup(func() { remoteCommandTimeout = old })
}

const (
	permissionDenied  = "root@ssh1.vast.ai: Permission denied (publickey)."
	connectionRefused = "ssh: connect to host ssh1.vast.ai port 14456: Connection refused"
	// vast.ai's shared ssh proxy emits this now and then under load.
	proxyDroppedUs = "kex_exchange_identification: Connection closed by remote host"
)

// steppedClock advances a fixed amount per read, which pins the grace period's
// boundary exactly: the Nth rejection is observed at (N-1) steps. Wall-clock
// timing would only ever let these tests assert "roughly", and "roughly" is what
// lets an off-by-one in the comparison through.
type steppedClock struct {
	at   time.Time
	step time.Duration
}

func (c *steppedClock) now() time.Time {
	c.at = c.at.Add(c.step)
	return c.at
}

// scriptedProbe replays reasons in order, repeating the last one forever, and
// counts calls. A nil reason means the login succeeded.
func scriptedProbe(reasons []string, thenSucceed bool, calls *int) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		*calls++
		if *calls > len(reasons) {
			if thenSucceed {
				return nil, nil
			}
			return []byte(reasons[len(reasons)-1]), fmt.Errorf("exit status 255")
		}
		return []byte(reasons[*calls-1]), fmt.Errorf("exit status 255")
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// The regression this guards: a host whose sshd refuses our key does so
// permanently, but WaitForSSH treated every failure as a race worth retrying and
// spent the deploy's whole 30-minute budget re-asking. Machine 55264 answered
// this way for 28 minutes — the model had downloaded and llama-server was
// serving on :8000 the entire time — because its /root/.ssh/authorized_keys had
// ownership or modes sshd would not accept.
func TestWaitForSSHFailsFastWhenTheKeyIsPermanentlyRejected(t *testing.T) {
	shrinkSSHTiming(t, 3*time.Second, time.Millisecond)

	// Deadline far beyond the grace period: reaching it means we waited it out.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	calls := 0
	clock := &steppedClock{step: time.Second}
	err := waitForSSH(ctx, "host:22", scriptedProbe(repeat(permissionDenied, 1), false, &calls), clock.now)

	if err == nil {
		t.Fatal("accepted a host that refuses our key")
	}
	// Typed, so DeployService can act on the diagnosis without parsing prose.
	if !errors.Is(err, ErrKeyRejected) {
		t.Errorf("not identifiable as a key rejection: %v", err)
	}
	// Rejection N is observed at (N-1) steps, so a 3s grace must fire on the 4th.
	if calls != 4 {
		t.Errorf("gave up after %d probes, want exactly 4 (grace 3s, 1s/probe)", calls)
	}
	// It must give up on the evidence, not on the deadline.
	if ctx.Err() != nil {
		t.Error("burned the deadline instead of failing fast")
	}
	// The point is that the host gets blamed, and DeployService.markHostBadUnlessCancelled
	// declines to blame exactly one thing — so this must not satisfy that test.
	if errors.Is(err, context.Canceled) {
		t.Errorf("reads as cancelled, the one error Deploy refuses to blame a host for: %v", err)
	}
}

// The other side of the same boundary, and the tolerance 09fa0f7 deliberately
// bought: vast.ai writes authorized_keys as the container comes up, so early
// rejections are a race, not a verdict. One step short of the grace must still
// wait.
func TestWaitForSSHStillToleratesAKeyThatArrivesJustInsideTheGrace(t *testing.T) {
	shrinkSSHTiming(t, 3*time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Three rejections put the clock at 2s of a 3s grace — the last moment at
	// which a late key is still allowed to save the deploy.
	calls := 0
	clock := &steppedClock{step: time.Second}
	err := waitForSSH(ctx, "host:22", scriptedProbe(repeat(permissionDenied, 3), true, &calls), clock.now)

	if err != nil {
		t.Fatalf("gave up on a key that landed one step inside the grace period: %v", err)
	}
	if calls != 4 {
		t.Errorf("took %d probes, want 4 (3 rejections then success)", calls)
	}
}

// The grace period belongs to the key, not to the deploy. A container that is
// still booting refuses TCP long before sshd offers publickey, and that time
// must not be charged to a key nobody has been asked about yet.
func TestWaitForSSHDoesNotSpendTheKeysGraceOnOtherFailures(t *testing.T) {
	shrinkSSHTiming(t, 3*time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	calls := 0
	clock := &steppedClock{step: time.Second}
	err := waitForSSH(ctx, "host:22",
		scriptedProbe(append(repeat(connectionRefused, 10), permissionDenied), false, &calls), clock.now)

	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("expected a key rejection verdict, got: %v", err)
	}
	// 10 refused probes must cost the key nothing: the verdict lands 4 rejections
	// later, not 4 probes in.
	if calls != 14 {
		t.Errorf("gave up after %d probes, want 14 — the boot-time refusals were charged to the key", calls)
	}
}

// The regression this guards is the first version of this fix, which required an
// *uninterrupted* run of rejections. vast.ai's ssh proxy drops a connection now
// and then; one such blip inside the window reset the clock, and on a flaky host
// the full-deadline burn came straight back.
func TestWaitForSSHVerdictSurvivesAnInterleavedTransient(t *testing.T) {
	shrinkSSHTiming(t, 3*time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Rejections at probes 1, 2, 4 — the proxy blips at 3. Time passes during the
	// blip like any other attempt, so probe 4 is 3 steps after the first
	// rejection and the verdict lands there: the transient cost the host nothing
	// and bought it nothing.
	calls := 0
	clock := &steppedClock{step: time.Second}
	err := waitForSSH(ctx, "host:22", scriptedProbe([]string{
		permissionDenied, permissionDenied, proxyDroppedUs, permissionDenied,
	}, false, &calls), clock.now)

	if !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("a single dropped connection postponed the verdict indefinitely: %v", err)
	}
	if calls != 4 {
		t.Errorf("gave up after %d probes, want 4 — the transient restarted the clock", calls)
	}
}

// captureStdout collects everything written to os.Stdout while fn runs. The
// heartbeat's entire value is the text it prints, so asserting on it is the only
// way to guard it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w

	// Drain concurrently: a heartbeat every 30 simulated seconds over a long run
	// can outgrow the pipe buffer, and a blocked write would deadlock fn.
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stdout = old
	w.Close()
	out := <-done
	r.Close()
	return out
}

// The regression this guards: "Waiting for SSH..." was the last line printed
// before a deploy went silent for as long as its deadline allowed. The download
// reporter cannot cover that stretch — it runs over SSH, so it only starts once
// the tunnel is up — which is why a 28-minute failure produced no output at all
// and looked like a hang.
func TestWaitForSSHReportsWhyItIsStillWaiting(t *testing.T) {
	shrinkSSHTiming(t, time.Hour, time.Millisecond) // grace out of reach: only the heartbeat is under test

	oldBeat := sshHeartbeatInterval
	sshHeartbeatInterval = 30 * time.Second
	t.Cleanup(func() { sshHeartbeatInterval = oldBeat })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	clock := &steppedClock{step: 10 * time.Second}
	out := captureStdout(t, func() {
		_ = waitForSSH(ctx, "host:22", func(context.Context) ([]byte, error) {
			calls++
			if calls > 8 {
				cancel() // 8 probes = 80 simulated seconds
			}
			return []byte(permissionDenied), fmt.Errorf("exit status 255")
		}, clock.now)
	})

	// Probe 1 sets the baseline; beats land once 30s have accrued since the last
	// one, i.e. at 30s, 60s — not on every attempt.
	if n := strings.Count(out, "still waiting for SSH"); n != 2 {
		t.Errorf("got %d heartbeats over 80s at a 30s interval, want 2:\n%s", n, out)
	}
	if !strings.Contains(out, "still waiting for SSH after 30s") {
		t.Errorf("heartbeat does not report elapsed time:\n%s", out)
	}
	// The reason is the whole point: "Permission denied" and "Connection refused"
	// call for completely different responses from whoever is watching.
	if !strings.Contains(out, permissionDenied) {
		t.Errorf("heartbeat does not say why, so it is just noise:\n%s", out)
	}
}

// Cancellation must reach an ssh already in flight, not wait out
// remoteCommandTimeout. DeployService refuses to blacklist on context.Canceled,
// so the error also has to stay recognisable as one.
func TestWaitForSSHHonoursCancellationDuringAProbe(t *testing.T) {
	shrinkSSHTiming(t, time.Hour, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	clock := &steppedClock{step: time.Second}

	err := waitForSSH(ctx, "host:22", func(ctx context.Context) ([]byte, error) {
		cancel() // the deploy is interrupted while this probe is running
		<-ctx.Done()
		return nil, fmt.Errorf("ssh cancelled: %w", ctx.Err())
	}, clock.now)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not survive to the caller, so a Ctrl-C would blacklist the host: %v", err)
	}
}

func TestKeyRejectedMatchesWhatSSHActuallyPrints(t *testing.T) {
	rejected := []string{
		// Observed verbatim from machine 55264.
		permissionDenied,
		// The parenthesised list is whatever the server offered.
		"root@host: Permission denied (publickey,password).",
		"Permission denied (publickey,keyboard-interactive).",
	}
	for _, s := range rejected {
		if !keyRejected(s) {
			t.Errorf("should be treated as a rejected key: %q", s)
		}
	}

	// Everything else is a transient the retry loop must keep waiting out.
	transient := []string{
		"ssh: connect to host host port 22: Connection refused",
		"ssh: connect to host host port 22: Connection timed out",
		"Connection closed by remote host",
		"kex_exchange_identification: Connection closed by remote host",
		"",
	}
	for _, s := range transient {
		if keyRejected(s) {
			t.Errorf("must not be treated as a permanent key rejection: %q", s)
		}
	}
}

// Every ssh entry point must authenticate identically, or the reachability probe
// proves nothing about the tunnel that follows it.
func TestBaseArgsSharedByProbeAndTunnel(t *testing.T) {
	args := strings.Join(baseArgs(2222), " ")

	for _, want := range []string{
		"StrictHostKeyChecking=no",
		// vast.ai recycles ssh1.vast.ai:PORT, so a remembered host key comes back
		// attached to a different instance and ssh hard-fails on the mismatch —
		// a permanent failure StrictHostKeyChecking=no does *not* cover.
		"UserKnownHostsFile=/dev/null",
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
	if key := identityFile(); key != "" {
		if !strings.Contains(args, "-i "+key) {
			t.Errorf("baseArgs does not pass the identity file %q: %s", key, args)
		}
		// Otherwise an agent holding more than six keys exhausts the server's
		// attempt limit before ours is ever offered.
		if !strings.Contains(args, "IdentitiesOnly=yes") {
			t.Errorf("baseArgs offers agent keys ahead of %q: %s", key, args)
		}
	}
}

// The predecessor of this function bound a port, closed it and returned the
// number. That is a snapshot, and the number is now used as a promise: it is
// printed before the GPU is rented and bound by ssh ten minutes later. Two
// concurrent deploys would both be handed the same port, announce the same URL,
// and the loser's tunnel would die on `bind: Address already in use` — which,
// during a deploy, destroys the instance it was tearing a tunnel down for.
func TestReservePortHoldsThePortUntilReleased(t *testing.T) {
	const base = 45000

	first, releaseFirst, err := ReservePort(0, base)
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	defer releaseFirst()

	second, releaseSecond, err := ReservePort(0, base)
	if err != nil {
		t.Fatalf("second reservation: %v", err)
	}
	defer releaseSecond()

	if second == first {
		t.Fatalf("both reservations got port %d — the first was not held", first)
	}

	// A pinned port that is taken has to fail now, not when ssh gets to it.
	if _, _, err := ReservePort(first, base); err == nil {
		t.Errorf("pinning the held port %d succeeded; --port must fail on a busy port", first)
	}

	// Releasing hands it over — this is what happens just before StartTunnel.
	releaseFirst()
	releaseFirst() // idempotent: callers defer it *and* call it explicitly
	third, releaseThird, err := ReservePort(first, base)
	if err != nil {
		t.Fatalf("reserving the released port %d: %v", first, err)
	}
	defer releaseThird()
	if third != first {
		t.Errorf("released port %d was not reusable, got %d", first, third)
	}
}

// A port genuinely in use by something else is the everyday case for --port.
func TestReservePortRejectsAPinnedPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	port, release, err := ReservePort(busy, 45000)
	if err == nil {
		release()
		t.Fatalf("reserved port %d while it was in use", port)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(busy)) {
		t.Errorf("error should name the port the user asked for: %v", err)
	}
}

// The scan is what makes concurrent instances work without the operator picking
// numbers: hold a run of ports and the next reservation steps past all of them.
func TestReservePortScansPastHeldPorts(t *testing.T) {
	const base = 46000

	var releases []func()
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		port, release, err := ReservePort(0, base)
		if err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
		releases = append(releases, release)
		if seen[port] {
			t.Fatalf("port %d handed out twice", port)
		}
		seen[port] = true
		if port < base || port >= base+portScanRange {
			t.Errorf("port %d outside the scan range %d-%d", port, base, base+portScanRange-1)
		}
	}
}

// StartTunnel owns the reservation handoff, and this is why: if the port is
// still held when ssh runs, ssh exits on `bind: Address already in use` — but
// the readiness dial below finds the *reservation's* listener, connects
// happily, and reports a working tunnel for a process that is already dead. The
// deploy then burns its whole deadline health-checking a forward that does not
// exist. Passing the release in makes the wrong order unrepresentable; this
// test proves the right one is what actually happens.
func TestStartTunnelReleasesTheReservationBeforeBinding(t *testing.T) {
	requireSSH(t)

	port, release, err := ReservePort(0, 47000)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	defer release()

	// ssh cannot reach a closed port, so it exits at once. What matters is that
	// the failure is *reported* rather than hidden behind the reservation.
	tun, err := StartTunnel(port, "127.0.0.1", closedPort(t), 8000, release)
	if err == nil {
		t.Fatalf("reported success for a tunnel that cannot exist: %+v", tun)
	}
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Errorf("failure not attributed to ssh exiting — the reservation may have answered the readiness dial: %v", err)
	}
	if conn, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond); derr == nil {
		conn.Close()
		t.Errorf("local port %d still answers: the reservation outlived the tunnel start", port)
	}
}

// A reservation is held for the whole deploy, so anything that tries the
// announced URL early must be turned away promptly. An unaccepted listener
// completes the handshake into its backlog and leaves the client hanging until
// its own timeout, which is a worse signal than a refusal.
func TestReservedPortDoesNotLeaveCallersHanging(t *testing.T) {
	port, release, err := ReservePort(0, 47500)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	defer release()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return // refused outright is fine too
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the reservation served data; it must only hang up")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Error("the reservation accepted a connection and left it hanging instead of closing it")
	}
}

// The reservation must bind exactly what `ssh -L` binds. The wildcard form used
// before is stricter than ssh: a service on another interface would make a
// perfectly usable port look taken.
func TestReservePortBindsLoopbackNotTheWildcard(t *testing.T) {
	port, release, err := ReservePort(0, 47800)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	defer release()

	// If the reservation had taken 0.0.0.0, this second bind would fail.
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err == nil {
		ln.Close()
		t.Errorf("port %d was reservable on 0.0.0.0 as well — the reservation is not loopback-only", port)
	}
}

// --port must not silently become something else, and the diagnosis has to name
// the real cause: bind fails with permission denied under 1024 just as readily
// as with address-in-use.
func TestReservePortRejectsPortsItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name string
		port int
		want string
	}{
		{"negative", -1, "invalid local port"},
		{"above the range", 70000, "invalid local port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port, release, err := ReservePort(tc.port, 48000)
			if err == nil {
				release()
				t.Fatalf("accepted %d and quietly returned %d", tc.port, port)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
