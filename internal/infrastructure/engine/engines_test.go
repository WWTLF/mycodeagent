package engine

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

// allEngines is every implementation MultiEngine can dispatch to. New engines go
// here, which is the point: the rules below are not llama.cpp's, they are the
// image-and-/proc rules every engine has to obey, and the ComfyUI and Jupyter
// engines shipped violating them because the original test named one engine.
func allEngines() map[string]EngineProvider {
	return map[string]EngineProvider{
		"llamacpp": NewLlamaCppEngine(),
		"comfyui":  NewComfyUIEngine(),
		"jupyter":  NewJupyterEngine(),
	}
}

// procPatternOf extracts the grep pattern out of a built command.
func procPatternOf(t *testing.T, cmd string) string {
	t.Helper()
	m := regexp.MustCompile(`grep -qs '([^']*)'`).FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("no quoted grep pattern in command: %s", cmd)
	}
	return m[1]
}

// The regression this guards, in the form that actually shipped: JupyterEngine
// used the bare pattern "jupyter". The command text lands in the argv of the
// shell that runs it, and /proc/<pid>/cmdline covers that shell, so the probe
// reported ALIVE with nothing running — and the kill sweep signalled its own SSH
// session. llama.cpp had spelled it "llama-serve[r]" for exactly this reason,
// but the test asserting it was pinned to NewLlamaCppEngine().
//
// Tested by execution, not by string matching: the liveness command is rewritten
// to scan only /proc/self/cmdline — the shell running it — and must print DEAD.
// That is precisely the self-match property, and it does not depend on what
// happens to be running on the machine.
func TestLivenessCommandsAreSelfMatchSafe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}
	for name, e := range allEngines() {
		t.Run(name, func(t *testing.T) {
			cmd := e.LivenessCommand(nil)
			selfOnly := strings.ReplaceAll(cmd, "/proc/*/cmdline", "/proc/self/cmdline")
			if selfOnly == cmd {
				t.Fatalf("command does not scan /proc/*/cmdline as expected: %s", cmd)
			}

			out, err := exec.Command("sh", "-c", selfOnly).Output()
			if err != nil {
				t.Fatalf("running probe: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != "DEAD" {
				t.Errorf("probe matched its own shell (reported %s); pattern %q is not self-match-safe:\n%s",
					got, procPatternOf(t, cmd), cmd)
			}
		})
	}
}

// The same property for the restart sweep, which is the dangerous half: a
// self-matching pattern there kills the SSH session issuing the command.
// Asserted with a regex rather than by running it, for obvious reasons.
func TestKillSweepsDoNotMatchThemselves(t *testing.T) {
	for name, e := range allEngines() {
		t.Run(name, func(t *testing.T) {
			killCmd, _ := e.RestartCommands(nil)
			pattern := procPatternOf(t, killCmd)
			re, err := regexp.Compile(pattern)
			if err != nil {
				t.Fatalf("pattern %q does not compile: %v", pattern, err)
			}
			if re.MatchString(killCmd) {
				t.Errorf("kill sweep matches its own command text and would signal the SSH session:\n%s", killCmd)
			}
		})
	}
}

// The image constraint: the llama.cpp server image is a bare CUDA runtime with
// no procps, so nothing may reach for pgrep/pkill/ps. Kept uniform across
// engines — one way to inspect processes is one place to get it wrong.
func TestEnginesNeverUseProcps(t *testing.T) {
	for name, e := range allEngines() {
		t.Run(name, func(t *testing.T) {
			killCmd, startCmd := e.RestartCommands(nil)
			for label, cmd := range map[string]string{
				"LivenessCommand":        e.LivenessCommand(nil),
				"DownloadedBytesCommand": e.DownloadedBytesCommand(nil),
				"killCmd":                killCmd,
				"startCmd":               startCmd,
			} {
				for _, banned := range []string{"pgrep", "pkill", "ps -"} {
					if strings.Contains(cmd, banned) {
						t.Errorf("%s uses %q, unavailable in the image: %s", label, banned, cmd)
					}
				}
			}
		})
	}
}

// A startup script that cannot fail is a startup script that reports success on
// a container where nothing came up. Both new engines shipped that way: ComfyUI
// polled for a supervisord that runtype "ssh" prevents from ever running, then
// exited 0 regardless, so the deploy proceeded to health-check a dead port until
// its deadline.
func TestOnstartScriptsFailLoudlyWhenTheServiceNeverAnswers(t *testing.T) {
	for name, e := range allEngines() {
		if name == "llamacpp" {
			// llama.cpp execs the server as PID 1 of the script; there is no
			// wait loop to get wrong, and a crash is caught by the watcher.
			continue
		}
		t.Run(name, func(t *testing.T) {
			onstart := e.BuildOnstart(&entity.Model{Name: "t"}, 1, 0, "")
			if !strings.Contains(onstart, "exit 1") {
				t.Errorf("onstart has no failure path — a container that started nothing exits 0:\n%s", onstart)
			}
			if !strings.Contains(onstart, "FATAL") {
				t.Errorf("onstart failure is silent; the log tail on abort would say nothing:\n%s", onstart)
			}
		})
	}
}

// Every engine must answer with its own port and health path for a nil model,
// because InstanceService resolves them for instances whose catalog entry may
// have been renamed since deploy.
func TestEnginePortsAndHealthPathsAreDistinctAndNilSafe(t *testing.T) {
	seen := map[int]string{}
	for name, e := range allEngines() {
		port := e.ServerPort(nil)
		if port <= 0 {
			t.Errorf("%s: ServerPort(nil) = %d, want the engine default", name, port)
		}
		if other, dup := seen[port]; dup {
			t.Errorf("%s and %s both claim port %d", name, other, port)
		}
		seen[port] = name

		if e.HealthPath(nil) == "" {
			t.Errorf("%s: HealthPath(nil) is empty, which WaitForServerHealth reads as llama.cpp's /v1/models", name)
		}
	}
}

// model.ServerPort / model.HealthPath must win over the engine default — that is
// the whole reason they exist on the entity.
func TestModelOverridesBeatEngineDefaults(t *testing.T) {
	m := &entity.Model{Name: "t", ServerPort: 9999, HealthPath: "/custom"}
	for name, e := range allEngines() {
		if got := e.ServerPort(m); got != 9999 {
			t.Errorf("%s: ServerPort = %d, want the model's 9999", name, got)
		}
		if got := e.HealthPath(m); got != "/custom" {
			t.Errorf("%s: HealthPath = %q, want the model's /custom", name, got)
		}
	}
}

// MultiEngine must route by EngineType, and must treat an unset type as
// llama.cpp so every pre-existing catalog row keeps working.
func TestMultiEngineRoutesByEngineType(t *testing.T) {
	m := NewMultiEngine()
	for _, tc := range []struct {
		engineType entity.EngineType
		wantPort   int
	}{
		{"", 8000}, // unset → llama.cpp, the backwards-compatible default
		{entity.EngineLlamaCpp, 8000},
		{entity.EngineComfyUI, comfyUIServerPort},
		{entity.EngineJupyter, jupyterServerPort},
		{"something-nobody-registered", 8000},
	} {
		model := &entity.Model{Name: "t", EngineType: tc.engineType}
		if got := m.ServerPort(model); got != tc.wantPort {
			t.Errorf("EngineType %q routed to port %d, want %d", tc.engineType, got, tc.wantPort)
		}
	}
}

// ComfyUI is reachable only through the SSH tunnel, and nothing in the image
// stands between the tunnel and the server: runtype "ssh" replaces the
// ENTRYPOINT, so the caddy/portal stack that would ask for a password never
// starts and BuildOnstart runs main.py directly.
//
// The engine used to set WEB_ENABLE_AUTH=false, USERNAME and PASSWORD to switch
// off the *ai-dock* image's auth. Those names mean nothing to vastai/comfy.
// Config that no longer has a consumer is worse than no config: it reads like a
// live safeguard, so the next person to touch this trusts a variable that has
// not done anything since the image changed.
func TestComfyUICarriesNoDeadAuthConfig(t *testing.T) {
	env := NewComfyUIEngine().EnvVars(nil)
	for _, dead := range []string{"WEB_ENABLE_AUTH", "USERNAME", "PASSWORD", "WEB_USER", "WEB_PASSWORD"} {
		if v, ok := env[dead]; ok {
			t.Errorf("%s=%q is set, but no process in this image reads it — the tunnel is the access control", dead, v)
		}
	}

	// The claim above only holds while we launch the server ourselves. If the
	// onstart ever defers to the image's boot sequence, auth comes back and this
	// test has to be revisited rather than deleted.
	onstart := NewComfyUIEngine().BuildOnstart(&entity.Model{Name: "comfyui"}, 1, 0, "")
	if !strings.Contains(onstart, "main.py") {
		t.Error("onstart no longer launches main.py directly; the image's auth stack may now apply")
	}
}

// Image references must be pinned and must have been checked against the
// registry. Both engines shipped with a tag that did not exist —
// ai-dock/comfyui has no 24.04 or 12.8 tag at all, and vastai/pytorch publishes
// no "cuda12" — so every deploy created a billing instance and then failed to
// pull. The build cannot verify a tag exists offline, but it can refuse the
// floating tags that make the failure intermittent instead of immediate.
func TestDockerImagesArePinned(t *testing.T) {
	for name, e := range allEngines() {
		img := e.DockerImage(nil)
		t.Run(name, func(t *testing.T) {
			repo, tag, ok := strings.Cut(img, ":")
			if !ok || tag == "" {
				t.Fatalf("image has no tag (would resolve to :latest): %q", img)
			}
			if repo == "" || !strings.Contains(repo, "/") {
				t.Errorf("image reference looks malformed: %q", img)
			}
			for _, floating := range []string{"latest", "main", "master", "edge", "nightly"} {
				if tag == floating {
					t.Errorf("tag %q floats — a silent upstream push would change what deploys: %q", tag, img)
				}
			}
		})
	}
}

// Both regressions cost a full rental on a live deploy, and both came from
// assuming the image's own boot sequence runs. Under runtype "ssh" vast.ai
// replaces the ENTRYPOINT, so /opt/ai-dock/bin/init.sh never executes.
func TestComfyUIOnstartDoesNotDependOnTheImageBootSequence(t *testing.T) {
	onstart := NewComfyUIEngine().BuildOnstart(&entity.Model{Name: "comfyui"}, 1, 0, "")

	// supervisord's caddy unit interpolates %(ENV_WORKSPACE)s. WORKSPACE is
	// exported by init.sh, so supervisord refuses to start on an unexpandable
	// name — and the deploy that preferred it never reached its fallback,
	// leaving nothing on 8188 until the deadline.
	if strings.Contains(onstart, "supervisord") {
		t.Error("onstart still reaches for supervisord, which cannot start under runtype ssh")
	}

	// WORKSPACE has to be set here instead, or provisioning scripts write models
	// relative to an empty path.
	if !strings.Contains(onstart, "WORKSPACE") {
		t.Error("onstart does not establish WORKSPACE")
	}

	// PROVISIONING_SCRIPT was delivered to the container and read by nobody:
	// init.sh is its only other consumer. `--provisioning` did nothing at all.
	if !strings.Contains(onstart, "PROVISIONING_SCRIPT") {
		t.Error("onstart never reads PROVISIONING_SCRIPT — the flag would be inert")
	}
	if !strings.Contains(onstart, "curl") || !strings.Contains(onstart, "provisioning.sh") {
		t.Error("onstart does not fetch and run the provisioning script")
	}

	// Ordering matters: models must land before the server opens, or the UI
	// comes up with an empty checkpoint list.
	// Match the launch itself, not the directory probe — that also mentions
	// main.py and comes first by design.
	provisionAt := strings.Index(onstart, "PROVISIONING_SCRIPT")
	launchAt := strings.Index(onstart, "nohup")
	if provisionAt < 0 || launchAt < 0 || provisionAt > launchAt {
		t.Errorf("provisioning must run before ComfyUI is launched (provision@%d launch@%d)", provisionAt, launchAt)
	}

	// A provisioning failure must not take the instance with it — an empty
	// ComfyUI you can add models to beats no ComfyUI at all.
	if !strings.Contains(onstart, "continuing without it") {
		t.Error("a failed provisioning script should be survivable, not fatal")
	}
}

// ComfyUI died on "No module named 'torch'" on a live deploy: the script picked
// the interpreter with `command -v python3`, which finds /usr/bin/python3, while
// ai-dock installs torch only into its own virtualenv. The image advertises the
// path in COMFYUI_VENV_PYTHON — the answer was in the environment all along.
func TestComfyUIOnstartUsesTheVenvInterpreter(t *testing.T) {
	onstart := NewComfyUIEngine().BuildOnstart(&entity.Model{Name: "comfyui"}, 1, 0, "")

	if !strings.Contains(onstart, "COMFYUI_VENV_PYTHON") {
		t.Error("onstart ignores COMFYUI_VENV_PYTHON, the path the image publishes")
	}
	// Whatever is chosen must be proven able to import torch, so a wrong guess
	// fails at selection time with a clear message instead of at import time
	// with a traceback the watcher has to surface.
	if !strings.Contains(onstart, "import torch") {
		t.Error("onstart does not verify the interpreter can import torch")
	}
	if !strings.Contains(onstart, "FATAL: no python with torch") {
		t.Error("onstart has no explicit failure when no usable interpreter exists")
	}
	// A bare `command -v python3` as the primary choice is the actual bug.
	venvAt := strings.Index(onstart, "COMFYUI_VENV_PYTHON")
	sysAt := strings.Index(onstart, "command -v python3")
	if venvAt < 0 || (sysAt >= 0 && sysAt < venvAt) {
		t.Error("the system interpreter is consulted before the venv one")
	}
}

// onstartPayload returns the startup script an onstart command would write to
// the instance.
//
// Every engine emits the same shape:
//
//	echo '<script>' > /tmp/x.sh && chmod +x /tmp/x.sh && bash /tmp/x.sh 2>&1 | tee …
//
// with each single quote in the script rewritten as '\” so it survives the
// surrounding quotes. The script is therefore *inside* a quoted string, which is
// the subtlety that matters for the test below: a syntax error in the script is
// invisible to anything parsing the command, because to a parser it is one long
// literal. Unwrapping is what makes it checkable.
//
// The boundary is the last `' > ` in the command. An escaped quote in the script
// can produce that sequence too, but only ever before this one — everything
// following it is the fixed tail of paths and pipes.
func onstartPayload(t *testing.T, onstart string) string {
	t.Helper()
	const open = "echo '"
	if !strings.HasPrefix(onstart, open) {
		t.Fatalf("onstart does not start with the expected wrapper: %.60s…", onstart)
	}
	end := strings.LastIndex(onstart, "' > ")
	if end < len(open) {
		t.Fatalf("onstart has no closing quote before its redirect: %.60s…", onstart)
	}
	return strings.ReplaceAll(onstart[len(open):end], `'\''`, "'")
}

// The onstart scripts are built by fmt.Sprintf and then quoted into a one-line
// command. A mistake in either half — an unbalanced `if`, a stray verb from a
// miscounted argument list — does not fail the build and does not fail any
// assertion about substrings. It is discovered when a rented GPU has already
// spent its whole startup budget on a script bash refused to parse.
//
// So parse them here, with `bash -n`, which reads for syntax and executes
// nothing.
//
// Checking the command as handed over is not enough, and the first version of
// this test made exactly that mistake: it passed `bash -n` the whole onstart,
// where the script is a single-quoted literal. Deleting a `fi` from the script
// left the test green, because the command around it still parsed. The script
// has to be unwrapped first — hence onstartPayload. Both layers are checked:
// the payload for the script's own syntax, the command for the quoting that
// carries it.
func TestOnstartScriptsAreValidShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	parses := func(t *testing.T, label, src string) {
		t.Helper()
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s is not valid shell: %v\n%s", label, err, out)
		}
	}

	for name, e := range allEngines() {
		t.Run(name, func(t *testing.T) {
			model := &entity.Model{Name: name, HFRepo: "org/repo", Quant: "Q4_K_M"}
			onstart := e.BuildOnstart(model, 1, 32768, "hf_token")

			// A miscounted Sprintf leaves %!s(MISSING) or %!d(EXTRA …) behind.
			// Those are valid shell — a word is a word — so bash -n reports
			// nothing and the instance runs a script with a hole in it.
			if i := strings.Index(onstart, "%!"); i >= 0 {
				end := i + 80
				if end > len(onstart) {
					end = len(onstart)
				}
				t.Errorf("onstart has a formatting error from a miscounted argument list: %q", onstart[i:end])
			}

			parses(t, "the startup script", onstartPayload(t, onstart))
			parses(t, "the onstart command", onstart)
		})
	}
}

// The directory an engine serves from and the directory it syncs back must be
// the same one.
//
// This is the defect that shipped: JupyterLab was launched with no root_dir, so
// under runtype "ssh" it served its working directory, /root — while SyncDirs
// watched /workspace, which the image never creates. The resolver found no
// candidate, the loop copied nothing, and every notebook died with the instance.
// Verified live before the fix: the lab process reported cwd -> /root and none
// of the three candidates existed.
//
// Asserting the pairing rather than the literal path, so moving the directory
// stays a one-line change that cannot desynchronise the two halves.
func TestEnginesSyncTheDirectoryTheyServeFrom(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine interface {
			BuildOnstart(*entity.Model, int, int, string) string
			SyncDirs(*entity.Model) []entity.SyncDir
		}
		model *entity.Model
	}{
		{"jupyter", NewJupyterEngine(), &entity.Model{Name: "j", EngineType: entity.EngineJupyter}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirs := tc.engine.SyncDirs(tc.model)
			if len(dirs) == 0 {
				t.Fatal("engine declares no SyncDirs")
			}
			script := tc.engine.BuildOnstart(tc.model, 1, 0, "")

			primary := dirs[0].RemoteCandidates[0]
			if !strings.Contains(script, primary) {
				t.Errorf("onstart never mentions the synced directory %q — the engine\n"+
					"writes somewhere the sync does not read:\n%s", primary, script)
			}
			// Existing is not enough: it has to be created, or the first pass finds
			// nothing and a two-way dir has nowhere to push.
			if !strings.Contains(script, "mkdir -p "+primary) {
				t.Errorf("onstart does not create %q; the sync resolver skips a\n"+
					"candidate that does not exist", primary)
			}
		})
	}
}

// Notebooks are source files the operator edits, and instances are disposable.
// Pull-only would rescue a session's work and then have no way to put it back on
// the next machine, so every deploy would start empty with the previous
// notebooks stranded locally.
func TestJupyterNotebooksSyncBothWays(t *testing.T) {
	dirs := NewJupyterEngine().SyncDirs(&entity.Model{Name: "j"})
	if len(dirs) == 0 {
		t.Fatal("jupyter declares no SyncDirs")
	}
	if !dirs[0].Push {
		t.Error("notebook directory is pull-only: local notebooks never reach a fresh instance")
	}
}

// The engine must chdir into the directory it serves before launching the
// server, not merely point --ServerApp.root_dir at it.
//
// root_dir moves only the file browser. The server process keeps the shell's
// working directory — /root under runtype "ssh" — and a kernel resolving its
// cwd from the process inherits it. Relative paths inside a notebook then aim
// at /root, so `Path("data/books").glob("*.txt")` returns nothing and a loader
// built on it yields an empty corpus *without raising*: the defect surfaces
// later and elsewhere, as a KeyError inside a tokenizer, with nothing pointing
// back at the working directory. Seen live: 35 files under /workspace/data/books,
// notebook reported 0 tokens.
//
// Asserting order — the chdir has to precede the launch, or it changes nothing.
func TestJupyterChdirsIntoItsWorkDirBeforeLaunch(t *testing.T) {
	engine := NewJupyterEngine()
	model := &entity.Model{Name: "j", EngineType: entity.EngineJupyter}
	script := engine.BuildOnstart(model, 1, 0, "")

	workDir := engine.SyncDirs(model)[0].RemoteCandidates[0]

	chdir := strings.Index(script, "cd "+workDir)
	if chdir < 0 {
		t.Fatalf("script never chdirs into %s; a kernel would inherit /root:\n%s", workDir, script)
	}
	launch := strings.Index(script, "lab --ip=")
	if launch < 0 {
		t.Fatal("script does not launch jupyter lab")
	}
	if chdir > launch {
		t.Errorf("chdir into %s comes after the launch (%d > %d): the server still starts in the shell's directory",
			workDir, chdir, launch)
	}
}
