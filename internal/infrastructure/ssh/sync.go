package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

// SyncRootName is the directory created in the working directory. Fixed rather
// than configurable so a user who ran `init` in one shell and `tunnel` in
// another still lands on the same place.
const SyncRootName = "COMFY_SYNC"

// syncIntervalSeconds is how long the loop sleeps between passes. rsync only
// transfers what changed, so a short interval is cheap; the cost of a long one
// is work lost when an instance dies unexpectedly.
const syncIntervalSeconds = 60

// StartSync launches a detached loop that rsyncs the engine's output directories
// into ./COMFY_SYNC, and returns its pid.
//
// It exists because instances are disposable and `kill` is the documented way to
// finish. That was free while llama.cpp was the only engine — it reads weights
// and produces nothing. ComfyUI generates images and stores workflows on the
// container disk, so without this, finishing normally destroys the work.
//
// A detached process rather than a goroutine, for the same reason the tunnel is
// one: the CLI exits after `init` and the sync has to outlive it.
func StartSync(sshHost string, sshPort int, dirs []entity.SyncDir, workDir string) (pid int, root string, err error) {
	if len(dirs) == 0 {
		return 0, "", nil // engine produces nothing worth keeping
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		return 0, "", fmt.Errorf("rsync not found on PATH: %w", err)
	}

	root = filepath.Join(workDir, SyncRootName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return 0, "", fmt.Errorf("create %s: %w", root, err)
	}

	script := buildSyncScript(sshHost, sshPort, dirs, root)
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // outlive the CLI
	// Detach stdio: this runs unattended, and a full pipe buffer would wedge it.
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, "", err
	}
	defer devNull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull

	if err := cmd.Start(); err != nil {
		return 0, "", fmt.Errorf("start sync loop: %w", err)
	}
	// Reap it so a loop that dies does not linger as a zombie — the same defect
	// the tunnel had.
	go func() { _ = cmd.Wait() }()

	return cmd.Process.Pid, root, nil
}

// StopSync terminates a sync loop and the whole process group, so the rsync or
// ssh it currently has running goes with it.
func StopSync(pid int) error {
	if pid <= 0 {
		return nil
	}
	// Negative pid signals the group. Setpgid put the loop in its own, so this
	// cannot reach anything else.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return syscall.Kill(pid, syscall.SIGTERM)
	}
	return nil
}

// buildSyncScript renders the loop.
//
// Remote paths are resolved on every pass rather than once at start: the
// directory may not exist until the engine first writes to it, and resolving
// once would then sync nothing forever.
//
// No --delete. Mirroring would mean a remote wipe — or a resolver that picks a
// different, empty candidate — silently erasing files already pulled to safety.
// Accumulating is the behaviour that cannot lose data.
func buildSyncScript(sshHost string, sshPort int, dirs []entity.SyncDir, root string) string {
	sshCmd := "ssh " + strings.Join(baseArgs(sshPort), " ")

	var b strings.Builder
	b.WriteString("while :; do\n")
	for _, d := range dirs {
		local := filepath.Join(root, d.Local)
		var probe strings.Builder
		for _, c := range d.RemoteCandidates {
			fmt.Fprintf(&probe, "[ -d %s ] && echo %s && break; ", shellQuote(c), shellQuote(c))
		}
		fmt.Fprintf(&b, "  REMOTE=$(%s root@%s %s 2>/dev/null | tr -d '\\r' | tail -1)\n",
			sshCmd, sshHost, shellQuote("for _ in 1; do "+probe.String()+"done"))
		fmt.Fprintf(&b, "  if [ -n \"$REMOTE\" ]; then mkdir -p %s && rsync -az --partial -e %s root@%s:\"$REMOTE\"/ %s/ >/dev/null 2>&1; fi\n",
			shellQuote(local), shellQuote(sshCmd), sshHost, shellQuote(local))
	}
	fmt.Fprintf(&b, "  sleep %d\ndone\n", syncIntervalSeconds)
	return b.String()
}

// shellQuote wraps a string in single quotes for safe interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
