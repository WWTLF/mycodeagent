package ssh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

// rsyncLines returns the rsync invocations in a generated script, in order.
func rsyncLines(script string) []string {
	var out []string
	for _, line := range strings.Split(script, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "rsync ") {
			out = append(out, t)
		}
	}
	return out
}

// A pull-only directory must move in exactly one direction. The engine marks
// output pull-only because the instance is its sole author; a stray upload would
// push local files onto the machine that generated them.
func TestPullOnlyDirSyncsDownOnly(t *testing.T) {
	dirs := []entity.SyncDir{{
		RemoteCandidates: []string{"/opt/app/output"},
		Local:            "output",
	}}
	script := buildSyncScript("h", 22, dirs, "/tmp/root")

	lines := rsyncLines(script)
	if len(lines) != 1 {
		t.Fatalf("want 1 rsync for a pull-only dir, got %d:\n%s", len(lines), script)
	}
	// Destination last: remote source, local destination. A lone SyncDir syncs
	// into the root itself — see TestSingleDirEngineSyncsIntoTheRootItself.
	if !strings.Contains(lines[0], `root@h:"$REMOTE"/ '/tmp/root'/`) {
		t.Errorf("pull-only rsync does not run remote → local: %s", lines[0])
	}
	// --update would mean a file whose local mtime happens to be ahead is never
	// pulled, and for a directory the remote alone writes there is nothing to
	// protect against.
	if strings.Contains(lines[0], "--update") {
		t.Errorf("pull-only rsync should not skip on local mtime: %s", lines[0])
	}
}

// The regression this exists for: workflows were pull-only, so a workflow edited
// locally never reached the instance that had to run it — local editing silently
// did nothing.
func TestPushDirSyncsBothWays(t *testing.T) {
	dirs := []entity.SyncDir{{
		RemoteCandidates: []string{"/opt/app/user/default/workflows"},
		Local:            "workflows",
		Push:             true,
		RootMarker:       "main.py",
	}}
	script := buildSyncScript("h", 22, dirs, "/tmp/root")

	lines := rsyncLines(script)
	if len(lines) != 2 {
		t.Fatalf("want 2 rsyncs for a two-way dir, got %d:\n%s", len(lines), script)
	}

	up, down := lines[0], lines[1]
	// Up first, so a fresh instance is seeded before anything is pulled back.
	if !strings.Contains(up, `'/tmp/root'/ root@h:"$REMOTE"/`) {
		t.Errorf("first rsync is not local → remote: %s", up)
	}
	if !strings.Contains(down, `root@h:"$REMOTE"/ '/tmp/root'/`) {
		t.Errorf("second rsync is not remote → local: %s", down)
	}
	for _, l := range lines {
		if !strings.Contains(l, "--update") {
			t.Errorf("two-way rsync without --update overwrites the newer copy: %s", l)
		}
	}
}

// Neither direction may delete. A resolver that lands on a different, empty
// candidate — or a remote wipe — would otherwise erase files already pulled to
// safety, and on the push side would erase the instance's work from a stale
// local copy.
func TestSyncNeverDeletes(t *testing.T) {
	dirs := []entity.SyncDir{
		{RemoteCandidates: []string{"/opt/app/output"}, Local: "output"},
		{RemoteCandidates: []string{"/opt/app/wf"}, Local: "workflows", Push: true, RootMarker: "main.py"},
	}
	script := buildSyncScript("h", 22, dirs, "/tmp/root")
	if strings.Contains(script, "--delete") {
		t.Errorf("sync script deletes; accumulating is the only behaviour that cannot lose data:\n%s", script)
	}
}

// The generated loop is handed to `sh -c` and runs unattended for the life of
// the instance. A syntax error there is invisible until images silently stop
// arriving.
func TestSyncScriptIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell available")
	}
	dirs := []entity.SyncDir{
		{RemoteCandidates: []string{"/opt/a/output", "/b/output"}, Local: "output"},
		{RemoteCandidates: []string{"/opt/a/wf", "/b/wf"}, Local: "workflows", Push: true, RootMarker: "main.py"},
	}
	script := buildSyncScript("h", 22, dirs, "/tmp/root")

	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("sync script is not valid shell: %v\n%s\n%s", err, out, script)
	}
}

// runResolver executes a resolver against the real filesystem and returns what
// it printed. The resolver is plain POSIX shell that runs on the instance, so
// running it here tests the thing itself rather than a description of it.
func runResolver(t *testing.T, d entity.SyncDir) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell available")
	}
	out, err := exec.Command(sh, "-c", remoteResolver(d)).Output()
	if err != nil {
		t.Fatalf("resolver failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// An existing candidate always wins, and nothing is created to reach it.
func TestResolverPrefersAnExistingCandidate(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "app", "output")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}

	got := runResolver(t, entity.SyncDir{
		RemoteCandidates: []string{filepath.Join(tmp, "absent", "output"), real},
		Local:            "output",
	})
	if got != real {
		t.Errorf("resolver picked %q, want the candidate that exists (%q)", got, real)
	}
	if _, err := os.Stat(filepath.Join(tmp, "absent", "output")); !os.IsNotExist(err) {
		t.Error("resolver created a candidate it did not need")
	}
}

// A two-way directory has to exist before anything can be pushed into it, and
// ComfyUI does not create user/default/workflows until a workflow is saved in
// the UI. For someone who only edits locally that never happens, so without
// this the directory stays one-way forever.
func TestResolverCreatesAPushTargetUnderTheAppRoot(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// The marker is what proves this tree is the real install.
	if err := os.WriteFile(filepath.Join(appRoot, "main.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(appRoot, "user", "default", "workflows")

	got := runResolver(t, entity.SyncDir{
		RemoteCandidates: []string{want},
		Local:            "workflows",
		Push:             true,
		RootMarker:       "main.py",
	})
	if got != want {
		t.Fatalf("resolver returned %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("resolver did not create the push target: %v", err)
	}
}

// The guard that makes creating a directory safe. A candidate list that has gone
// stale against the image must resolve to nothing, not to a plausible-looking
// path: a sync running for the life of the instance into somewhere nothing reads
// looks exactly like a working sync until the work is gone.
func TestResolverWillNotCreateOutsideAnAppRoot(t *testing.T) {
	tmp := t.TempDir()
	stale := filepath.Join(tmp, "wrong-layout", "user", "default", "workflows")

	got := runResolver(t, entity.SyncDir{
		RemoteCandidates: []string{stale},
		Local:            "workflows",
		Push:             true,
		RootMarker:       "main.py",
	})
	if got != "" {
		t.Errorf("resolver returned %q for a tree with no marker; want nothing", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("resolver created a directory in a tree it could not identify")
	}
}

// A pull-only directory must never create anything remotely, marker or not — it
// has nothing to write there, and waiting is correct until the engine writes.
func TestResolverNeverCreatesForAPullOnlyDir(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "main.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(appRoot, "output")

	got := runResolver(t, entity.SyncDir{
		RemoteCandidates: []string{absent},
		Local:            "output",
		RootMarker:       "main.py", // set, but Push is not
	})
	if got != "" {
		t.Errorf("resolver returned %q for a pull-only dir that does not exist yet", got)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Error("pull-only resolver created a remote directory")
	}
}

// --sync-folder must survive being handed to a command that runs somewhere else.
// The root is stored on the instance and reused by `tunnel` and `start`, so a
// path left relative would re-resolve against whichever shell ran the later
// command and quietly split one instance's files across two directories.
func TestResolveSyncRootReturnsAbsolutePaths(t *testing.T) {
	wd := t.TempDir()
	restore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })

	got, err := ResolveSyncRoot("notebooks")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("relative --sync-folder stayed relative: %q", got)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+"notebooks") {
		t.Errorf("resolved path lost the folder name: %q", got)
	}
}

// An empty flag keeps the historical default, so instances deployed before
// --sync-folder existed land where their loops already point.
func TestResolveSyncRootDefaultsToTheWorkingDirectory(t *testing.T) {
	wd := t.TempDir()
	restore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })

	got, err := ResolveSyncRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != SyncRootName {
		t.Errorf("default root is %q, want a directory named %q", got, SyncRootName)
	}
}

// "~/notebooks" reaches the flag literally whenever it is quoted, and a literal
// "~" directory in the working directory is never what was meant.
func TestResolveSyncRootExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	got, err := ResolveSyncRoot("~/notebooks")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "notebooks"); got != want {
		t.Errorf("ResolveSyncRoot(\"~/notebooks\") = %q, want %q", got, want)
	}
}

// An absolute --sync-folder is used as given: it is the root itself, not a
// parent to create the default folder inside. Nesting would make the flag's
// own value not the directory the files appear in.
func TestResolveSyncRootUsesAnAbsoluteFolderAsTheRoot(t *testing.T) {
	got, err := ResolveSyncRoot("/tmp/nb")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/nb" {
		t.Errorf("ResolveSyncRoot(\"/tmp/nb\") = %q, want /tmp/nb", got)
	}
}

// The bug this exists for, in the form it was hit: `--sync-folder .` in a
// directory full of notebooks. The root was the project, but SyncDir.Local was
// appended unconditionally, so the loop watched ./workspace/ — an empty folder
// created beside the notebooks. The files sat one level above the only directory
// being synced, so nothing moved in either direction, and nothing failed or was
// logged. It reads as a broken sync rather than a misplaced folder.
//
// An engine with a single directory therefore syncs into the root itself.
func TestSingleDirEngineSyncsIntoTheRootItself(t *testing.T) {
	dirs := []entity.SyncDir{{
		RemoteCandidates: []string{"/workspace"},
		Local:            "workspace",
		Push:             true,
	}}
	script := buildSyncScript("h", 22, dirs, "/home/me/project")

	for _, l := range rsyncLines(script) {
		if !strings.Contains(l, `'/home/me/project'/`) {
			t.Errorf("rsync does not use the root itself: %s", l)
		}
		if strings.Contains(l, "/home/me/project/workspace") {
			t.Errorf("rsync still appends the subfolder: %s", l)
		}
	}
}

// The other half of the rule, and the reason it is conditional rather than
// absolute. ComfyUI pulls output/ down and pushes workflows/ up; merged into one
// directory, the push leg would upload every generated image into the instance's
// workflow folder on the next pass.
func TestMultiDirEngineKeepsItsSubfolders(t *testing.T) {
	dirs := []entity.SyncDir{
		{RemoteCandidates: []string{"/app/output"}, Local: "output"},
		{RemoteCandidates: []string{"/app/wf"}, Local: "workflows", Push: true, RootMarker: "main.py"},
	}
	script := buildSyncScript("h", 22, dirs, "/home/me/project")

	for _, want := range []string{"/home/me/project/output", "/home/me/project/workflows"} {
		if !strings.Contains(script, want) {
			t.Errorf("script does not sync into %s:\n%s", want, script)
		}
	}
	// Neither leg may target the bare root, or the two directories become one.
	for _, l := range rsyncLines(script) {
		if strings.Contains(l, `'/home/me/project'/`) {
			t.Errorf("a multi-dir engine collapsed into the root: %s", l)
		}
	}
}

// The default root is the flag's default too, so they cannot drift apart.
func TestDefaultSyncRootIsWorkspaceUnderCwd(t *testing.T) {
	if entity.DefaultSyncRootName != "workspace" {
		t.Errorf("default root name is %q, want \"workspace\"", entity.DefaultSyncRootName)
	}
	got, err := ResolveSyncRoot("")
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if want := filepath.Join(wd, "workspace"); got != want {
		t.Errorf("ResolveSyncRoot(\"\") = %q, want %q", got, want)
	}
}

// "." is an ordinary relative path and must resolve to the working directory
// itself — the case the flag is most often typed with.
func TestResolveSyncRootAcceptsDot(t *testing.T) {
	got, err := ResolveSyncRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if got != wd {
		t.Errorf("ResolveSyncRoot(\".\") = %q, want the working directory %q", got, wd)
	}
}

// The sync must not carry a project's virtualenv to the instance.
//
// `--sync-folder .` points at a project, and a Python project keeps its venv
// inside it — 4.8 GB of CUDA PyTorch here against 100 MB of real work. rsync
// copies in directory order, so .venv went first and the data the notebooks
// read had not arrived eight minutes in: the kernel failed on a data directory
// that existed and was empty, which reads as a broken path, not a slow copy.
//
// It is also useless on arrival. The image has its own interpreter at
// /venv/main, and a venv built on another machine has that machine's paths
// baked into its scripts.
func TestSyncScriptExcludesVirtualenvsAndRepoJunk(t *testing.T) {
	script := buildSyncScript("host", 22,
		[]entity.SyncDir{{RemoteCandidates: []string{"/workspace"}, Local: "workspace", Push: true}},
		"/local/project")

	for _, pattern := range []string{".venv", "__pycache__", ".git", "node_modules", ".ipynb_checkpoints"} {
		if !strings.Contains(script, "--exclude="+shellQuote(pattern)) {
			t.Errorf("sync script does not exclude %q:\n%s", pattern, script)
		}
	}

	// Both directions, or the venv arrives on the way back instead.
	if n := strings.Count(script, "--exclude="+shellQuote(".venv")); n != 2 {
		t.Errorf("expected .venv excluded on push and pull (2 occurrences), got %d", n)
	}
}
