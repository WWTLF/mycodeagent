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

// SyncRootName is the directory created in the working directory when no
// explicit root is chosen. Defined in the domain so the command and application
// layers can name it without importing this package.
const SyncRootName = entity.DefaultSyncRootName

// ResolveSyncRoot turns a user-supplied --sync-folder into the absolute
// directory the loop will use, falling back to <cwd>/COMFY_SYNC.
//
// Absolute is the whole point. The root is stored on the instance and reused by
// `tunnel` and `start`, which routinely run from a different directory than
// `init` did; keeping a relative path would resolve it against whichever shell
// ran the later command and quietly sync into a second location. "~" is expanded
// here too — the flag is often typed unquoted, but not always, and a literal
// "~" directory in cwd is never what was meant.
func ResolveSyncRoot(folder string) (string, error) {
	if strings.TrimSpace(folder) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory for sync: %w", err)
		}
		return filepath.Join(wd, SyncRootName), nil
	}

	folder = strings.TrimSpace(folder)
	if folder == "~" || strings.HasPrefix(folder, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", folder, err)
		}
		folder = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(folder, "~"), "/"))
	}

	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", folder, err)
	}
	return abs, nil
}

// syncIntervalSeconds is how long the loop sleeps between passes. rsync only
// transfers what changed, so a short interval is cheap; the cost of a long one
// is work lost when an instance dies unexpectedly.
const syncIntervalSeconds = 60

// StartSync launches a detached loop that keeps the engine's directories in step
// with ./COMFY_SYNC, and returns its pid. Most are pulled down only; the ones
// the engine marks Push travel both ways.
//
// It exists because instances are disposable and `kill` is the documented way to
// finish. That was free while llama.cpp was the only engine — it reads weights
// and produces nothing. ComfyUI generates images and stores workflows on the
// container disk, so without this, finishing normally destroys the work.
//
// A detached process rather than a goroutine, for the same reason the tunnel is
// one: the CLI exits after `init` and the sync has to outlive it.
//
// syncRoot is the directory to sync into, "" for the default. It is returned
// resolved so the caller can persist the choice rather than re-deriving it.
func StartSync(sshHost string, sshPort int, dirs []entity.SyncDir, syncRoot string) (pid int, root string, err error) {
	if len(dirs) == 0 {
		return 0, "", nil // engine produces nothing worth keeping
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		return 0, "", fmt.Errorf("rsync not found on PATH: %w", err)
	}

	root, err = ResolveSyncRoot(syncRoot)
	if err != nil {
		return 0, "", err
	}
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
// No --delete, in either direction. Mirroring would mean a remote wipe — or a
// resolver that picks a different, empty candidate — silently erasing files
// already pulled to safety. Accumulating is the behaviour that cannot lose data.
// The cost is that deleting a file only on one side brings it back on the next
// pass; deleting it on both is what sticks.
//
// Two-way directories additionally pass --update in both directions, so each
// side refuses to overwrite a file the other has a newer copy of. Two people
// editing the same workflow at the same second is not a case this resolves — it
// resolves the case that actually happens, which is edits on one side at a time,
// and it fails by leaving a file alone rather than by destroying it.
func buildSyncScript(sshHost string, sshPort int, dirs []entity.SyncDir, root string) string {
	sshCmd := "ssh " + strings.Join(baseArgs(sshPort), " ")

	var b strings.Builder
	b.WriteString("while :; do\n")
	for _, d := range dirs {
		local := filepath.Join(root, d.Local)

		fmt.Fprintf(&b, "  REMOTE=$(%s root@%s %s 2>/dev/null | tr -d '\\r' | tail -1)\n",
			sshCmd, sshHost, shellQuote(remoteResolver(d)))

		// --update protects a local edit from being clobbered by an older remote
		// copy. It is confined to two-way directories: for a pull-only one the
		// remote is the sole author, and skipping a file because the local mtime
		// happens to be ahead would mean never pulling it.
		flags := "-az --partial"
		if d.Push {
			flags += " --update"
		}

		fmt.Fprintf(&b, "  if [ -n \"$REMOTE\" ]; then\n")
		fmt.Fprintf(&b, "    mkdir -p %s\n", shellQuote(local))
		if d.Push {
			// Up before down, so a fresh instance is seeded with what is already
			// on disk here before anything is pulled back.
			fmt.Fprintf(&b, "    rsync %s -e %s %s/ root@%s:\"$REMOTE\"/ >/dev/null 2>&1\n",
				flags, shellQuote(sshCmd), shellQuote(local), sshHost)
		}
		fmt.Fprintf(&b, "    rsync %s -e %s root@%s:\"$REMOTE\"/ %s/ >/dev/null 2>&1\n",
			flags, shellQuote(sshCmd), sshHost, shellQuote(local))
		fmt.Fprintf(&b, "  fi\n")
	}
	fmt.Fprintf(&b, "  sleep %d\ndone\n", syncIntervalSeconds)
	return b.String()
}

// remoteResolver renders the shell run on the instance to pick the directory to
// sync against. It prints the path, or nothing.
//
// First choice is always a candidate that already exists. A push-only second
// pass then creates the directory, because a push has nowhere to write until it
// does and a workflows directory does not appear until the first workflow is
// saved on the instance — which for someone editing them locally may be never,
// leaving a two-way directory permanently one-way.
//
// Creating a path is only safe when it is known to be the right one, so the
// second pass requires the candidate to sit under a tree containing the marker
// file. Without that check a stale candidate list would create a plausible
// directory in an image that keeps its files elsewhere, and the sync would then
// run for the life of the instance writing where nothing reads.
func remoteResolver(d entity.SyncDir) string {
	var b strings.Builder
	b.WriteString("R=''; ")
	for _, c := range d.RemoteCandidates {
		fmt.Fprintf(&b, "[ -z \"$R\" ] && [ -d %s ] && R=%s; ", shellQuote(c), shellQuote(c))
	}

	if d.Push && d.RootMarker != "" {
		b.WriteString("if [ -z \"$R\" ]; then for c in ")
		for i, c := range d.RemoteCandidates {
			if i > 0 {
				b.WriteString(" ")
			}
			b.WriteString(shellQuote(c))
		}
		// Walk up from the candidate looking for the marker; the loop stops at /
		// so an absent marker terminates rather than spinning.
		fmt.Fprintf(&b, "; do d=\"$c\"; while [ -n \"$d\" ] && [ \"$d\" != / ]; do "+
			"if [ -e \"$d/%s\" ]; then mkdir -p \"$c\" && R=\"$c\"; break; fi; "+
			"d=$(dirname \"$d\"); done; [ -n \"$R\" ] && break; done; fi; ", d.RootMarker)
	}

	b.WriteString("printf '%s\\n' \"$R\"")
	return b.String()
}

// shellQuote wraps a string in single quotes for safe interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
