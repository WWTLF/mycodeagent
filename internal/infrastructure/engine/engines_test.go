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

// ComfyUI is reachable only through the SSH tunnel, so its own HTTP auth just
// locks the operator out. The engine used to *describe* these variables in a
// comment while setting nothing.
func TestComfyUIDisablesItsOwnWebAuth(t *testing.T) {
	env := NewComfyUIEngine().EnvVars(nil)
	if len(env) == 0 {
		t.Fatal("ComfyUI sets no environment; the image's auth defaults apply behind the tunnel")
	}
	if v, ok := env["WEB_ENABLE_AUTH"]; !ok || v != "false" {
		t.Errorf("WEB_ENABLE_AUTH = %q (present: %v), want \"false\"", v, ok)
	}
}
