package engine

import (
	"strings"
	"testing"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/service"
)

// The engine must satisfy the domain interface; nothing else wires it up in tests.
var _ service.EngineProvider = (*LlamaCppEngine)(nil)

func testModel() *entity.Model {
	return &entity.Model{
		Name:      "qwen3-8b",
		HFRepo:    "unsloth/Qwen3-8B-GGUF",
		Quant:     "UD-Q4_K_XL",
		LlamaArgs: []string{"--jinja", "-fa", "on", "--cache-type-k", "q8_0"},
	}
}

func TestServeArgsInjectsRuntimeValues(t *testing.T) {
	got := strings.Join(NewLlamaCppEngine().serveArgs(testModel(), 1, 65536), " ")

	for _, want := range []string{
		"-hf unsloth/Qwen3-8B-GGUF:UD-Q4_K_XL",
		"--alias qwen3-8b",
		"--host 0.0.0.0",
		"--port 8000",
		"-ngl 999",
		"--ctx-size 65536",
		"--jinja",
		"-fa on",
		"--cache-type-k q8_0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestServeArgsOmitsQuantWhenUnset(t *testing.T) {
	m := testModel()
	m.Quant = ""
	got := strings.Join(NewLlamaCppEngine().serveArgs(m, 1, 0), " ")

	if !strings.Contains(got, "-hf unsloth/Qwen3-8B-GGUF ") {
		t.Errorf("expected bare repo reference, got:\n%s", got)
	}
	// contextLength 0 means "let llama.cpp use the model default" — no --ctx-size.
	if strings.Contains(got, "--ctx-size") {
		t.Errorf("expected no --ctx-size for contextLength 0, got:\n%s", got)
	}
}

// A catalog entry must never be able to override a flag the engine derives from
// runtime facts, or the search filter and the launched server would drift apart.
func TestServeArgsStripsEngineOwnedFlagsFromCatalog(t *testing.T) {
	m := testModel()
	m.LlamaArgs = []string{
		"-c", "999999",
		"--ctx-size", "888888",
		"-ngl", "5",
		"--n-gpu-layers", "6",
		"-a", "hijacked",
		"--alias", "hijacked2",
		"--port", "1234",
		"--host", "127.0.0.1",
		"-hf", "someone/else-GGUF",
		"--hf-file", "other.gguf",
		"-m", "/local/path.gguf",
		"--jinja", // not engine-owned: must survive
	}

	got := strings.Join(NewLlamaCppEngine().serveArgs(m, 1, 65536), " ")

	for _, unwanted := range []string{
		"999999", "888888", "hijacked", "1234", "127.0.0.1",
		"someone/else-GGUF", "other.gguf", "/local/path.gguf",
		"-ngl 5", "-ngl 6",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("catalog override %q leaked into args:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "--jinja") {
		t.Errorf("non-engine-owned flag was stripped:\n%s", got)
	}
	if strings.Count(got, "--ctx-size") != 1 || !strings.Contains(got, "--ctx-size 65536") {
		t.Errorf("expected exactly one engine-supplied --ctx-size 65536:\n%s", got)
	}
	if strings.Count(got, "--alias") != 1 || !strings.Contains(got, "--alias qwen3-8b") {
		t.Errorf("expected exactly one engine-supplied --alias:\n%s", got)
	}
}

func TestServeArgsSplitMode(t *testing.T) {
	e := NewLlamaCppEngine()

	if got := strings.Join(e.serveArgs(testModel(), 1, 4096), " "); strings.Contains(got, "--split-mode") {
		t.Errorf("single GPU should not set --split-mode:\n%s", got)
	}
	if got := strings.Join(e.serveArgs(testModel(), 2, 4096), " "); !strings.Contains(got, "--split-mode layer") {
		t.Errorf("multi GPU should set --split-mode layer:\n%s", got)
	}

	// An explicit catalog choice wins — the engine must not emit a second one.
	m := testModel()
	m.LlamaArgs = []string{"-sm", "row"}
	got := strings.Join(e.serveArgs(m, 2, 4096), " ")
	if strings.Contains(got, "--split-mode") {
		t.Errorf("catalog -sm should suppress the engine default:\n%s", got)
	}
}

// `-hf` downloads through the HuggingFace client, so the cache is the HF layout —
// NOT ~/.cache/llama.cpp, which the docs claimed until a live deploy showed the
// file under /root/.cache/huggingface/hub/models--<org>--<repo>/blobs/. Pointing
// the probe at the wrong directory would report 0 bytes forever, i.e. exactly the
// "looks hung" symptom it exists to remove.
func TestDownloadedBytesCommandReadsTheHuggingFaceCache(t *testing.T) {
	cmd := NewLlamaCppEngine().DownloadedBytesCommand(testModel())

	if !strings.Contains(cmd, "/root/.cache/huggingface") {
		t.Errorf("probe does not read the HF cache: %s", cmd)
	}
	if strings.Contains(cmd, "llama.cpp") {
		t.Errorf("probe reads the wrong cache directory: %s", cmd)
	}
	// Must print a bare number even when the directory does not exist yet.
	if !strings.Contains(cmd, "echo 0") {
		t.Errorf("probe has no zero fallback before the download starts: %s", cmd)
	}
}

// Restart strips the trailing " && bash ..." to re-write the script without
// running it, so BuildOnstart must keep that exact separator in place.
func TestBuildOnstartShapeSupportsRestartRewrite(t *testing.T) {
	onstart := NewLlamaCppEngine().BuildOnstart(testModel(), 1, 65536, "")

	idx := strings.LastIndex(onstart, " && bash ")
	if idx <= 0 {
		t.Fatalf("onstart has no ' && bash ' separator for Restart to split on:\n%s", onstart)
	}
	writeOnly := onstart[:idx]
	if !strings.HasPrefix(writeOnly, "echo '") {
		t.Errorf("write-only prefix is not an echo redirect:\n%s", writeOnly)
	}
	if strings.Contains(writeOnly, "| tee") {
		t.Errorf("write-only prefix still executes/tees:\n%s", writeOnly)
	}
	if !strings.Contains(onstart, logPath) {
		t.Errorf("onstart does not tee to %s:\n%s", logPath, onstart)
	}
	// The image has no /app on PATH, so the script must fall back to the abs path.
	if !strings.Contains(onstart, binaryPath) {
		t.Errorf("onstart does not resolve %s:\n%s", binaryPath, onstart)
	}
}

// Single quotes from catalog args must survive the `echo '...'` wrapper: an
// unescaped one would close the echo string early and write a mangled script to
// the instance, which only shows up as a failed deploy on a paid GPU.
func TestBuildOnstartEscapesSingleQuotes(t *testing.T) {
	m := testModel()
	m.LlamaArgs = []string{"--chat-template-kwargs", `'{"enable_thinking":false}'`}
	e := NewLlamaCppEngine()

	onstart := e.BuildOnstart(m, 1, 4096, "")

	const prefix = "echo '"
	end := strings.LastIndex(onstart, "' > "+scriptPath)
	if !strings.HasPrefix(onstart, prefix) || end < len(prefix) {
		t.Fatalf("unexpected onstart shape:\n%s", onstart)
	}
	payload := onstart[len(prefix):end]

	// Every quote must be part of a '\'' sequence — nothing bare may remain.
	if stray := strings.ReplaceAll(payload, `'\''`, ""); strings.Contains(stray, "'") {
		t.Errorf("payload has an unescaped single quote:\n%s", payload)
	}
	// And unescaping must reproduce the script byte for byte, which is what bash
	// writes to the file.
	want := e.buildScript(m, 1, 4096)
	if got := strings.ReplaceAll(payload, `'\''`, "'"); got != want {
		t.Errorf("payload does not round-trip to the script:\ngot:  %q\nwant: %q", got, want)
	}
}

// The liveness probe and kill sweep run over /proc because the server-cuda image
// ships no procps. They must also not match the shell that runs them: the command
// text lands in that shell's own /proc/<pid>/cmdline.
func TestProcessCommandsAvoidProcpsAndSelfMatch(t *testing.T) {
	e := NewLlamaCppEngine()
	killCmd, startCmd := e.RestartCommands(testModel())

	for name, cmd := range map[string]string{
		"LivenessCommand": e.LivenessCommand(testModel()),
		"killCmd":         killCmd,
	} {
		if strings.Contains(cmd, "pgrep") || strings.Contains(cmd, "pkill") || strings.Contains(cmd, "ps -") {
			t.Errorf("%s uses procps tooling unavailable in the image: %s", name, cmd)
		}
		if !strings.Contains(cmd, "/proc/") {
			t.Errorf("%s does not scan /proc: %s", name, cmd)
		}
		if !strings.Contains(cmd, procPattern) {
			t.Errorf("%s does not use the bracketed self-match-safe pattern: %s", name, cmd)
		}
		// The literal process name must never appear unbracketed, or the command
		// matches its own shell.
		if strings.Contains(cmd, "llama-server") {
			t.Errorf("%s contains an unbracketed 'llama-server' and will self-match: %s", name, cmd)
		}
	}

	// The progress probe runs on the same bare image and must obey the same rules.
	if dl := e.DownloadedBytesCommand(testModel()); strings.Contains(dl, "pgrep") || strings.Contains(dl, "pkill") {
		t.Errorf("DownloadedBytesCommand uses procps tooling: %s", dl)
	}

	if !strings.Contains(killCmd, "kill -9 ") {
		t.Errorf("killCmd has no SIGKILL escalation: %s", killCmd)
	}
	if !strings.Contains(startCmd, scriptPath) || !strings.Contains(startCmd, logPath) {
		t.Errorf("startCmd should re-run %s and tee to %s: %s", scriptPath, logPath, startCmd)
	}
}
