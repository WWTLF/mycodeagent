package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

const (
	// serverPort is the port llama-server listens on inside the container. The
	// SSH tunnel maps a local port onto it, so this must match the tunnel setup.
	serverPort = 8000

	// scriptPath / logPath live on the ephemeral container disk.
	scriptPath = "/tmp/start_llama.sh"
	logPath    = "/tmp/llama.log"

	// binaryPath is where the server-cuda image keeps llama-server. Its PATH does
	// NOT include /app (only the image ENTRYPOINT relies on WORKDIR), so the
	// startup script resolves the binary itself instead of assuming a bare
	// `llama-server` resolves.
	binaryPath = "/app/llama-server"

	// cacheDir is where `-hf` puts the GGUF. It is the HuggingFace client's
	// layout, not llama.cpp's own ~/.cache/llama.cpp — confirmed on a live
	// instance, where the file landed under
	// /root/.cache/huggingface/hub/models--<org>--<repo>/blobs/.
	cacheDir = "/root/.cache/huggingface"

	// procPattern matches llama-server in /proc/<pid>/cmdline. The last character
	// is bracketed so the pattern never matches the process running the check:
	// the command text itself sits in that shell's argv, which /proc/*/cmdline
	// includes. Without the brackets the liveness probe would always report ALIVE
	// and the kill sweep would shoot the SSH session.
	procPattern = "llama-serve[r]"
)

// LlamaCppEngine implements service.EngineProvider for llama.cpp's llama-server.
//
// The image is the CUDA server build from ggml-org. It is a plain
// nvidia/cuda:*-runtime-ubuntu base plus libgomp1/curl/ffmpeg — notably WITHOUT
// procps, so nothing here may use pgrep/pkill; process checks read /proc directly.
type LlamaCppEngine struct{}

func NewLlamaCppEngine() *LlamaCppEngine {
	return &LlamaCppEngine{}
}

// DockerImage returns the pinned llama.cpp server image. Bump the bXXXXX build
// number to upgrade; tags are listed at ghcr.io/ggml-org/llama.cpp.
func (e *LlamaCppEngine) DockerImage() string {
	return "ghcr.io/ggml-org/llama.cpp:server-cuda-b10156"
}

func (e *LlamaCppEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	// hfToken is passed to the instance as HF_TOKEN via the environment (see
	// DeployService.engineEnv); llama-server reads it from there for gated repos.
	script := e.buildScript(model, numGPUs, contextLength)
	// Escape single quotes so the script survives being wrapped in `echo '...'`.
	escaped := strings.ReplaceAll(script, "'", `'\''`)
	return fmt.Sprintf("echo '%s' > %s && chmod +x %s && bash %s 2>&1 | tee %s",
		escaped, scriptPath, scriptPath, scriptPath, logPath)
}

func (e *LlamaCppEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	return binaryPath + " " + strings.Join(e.serveArgs(model, numGPUs, contextLength), " ")
}

func (e *LlamaCppEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	// llama-server is a single process — unlike vLLM there are no worker
	// subprocesses holding VRAM — so TERM, then a KILL sweep for anything that
	// ignored it, is enough.
	killCmd = strings.Join([]string{
		procKill(""),
		"sleep 3",
		procKill("-9"),
		"sleep 1",
	}, "; ")
	startCmd = fmt.Sprintf("nohup bash %s 2>&1 | tee %s &", scriptPath, logPath)
	return
}

// LivenessCommand returns a shell command that echoes ALIVE or DEAD depending on
// whether llama-server is still running.
func (e *LlamaCppEngine) LivenessCommand() string {
	return fmt.Sprintf("grep -qs '%s' /proc/*/cmdline && echo ALIVE || echo DEAD", procPattern)
}

// DownloadedBytesCommand prints how many bytes of the model have landed in the
// cache so far, or 0 before it exists.
//
// `-hf` downloads through the HuggingFace client, so the cache is the HF layout
// (models--<org>--<repo>/blobs/…) and NOT ~/.cache/llama.cpp — verified on a live
// deploy. The in-progress blob is a sibling `.downloadInProgress` file, and `du`
// counts it, which is exactly what makes this usable as a progress signal:
// llama.cpp prints nothing at all while downloading, so the log looks identical
// to a hang for however many minutes the GGUF takes.
func (e *LlamaCppEngine) DownloadedBytesCommand() string {
	return fmt.Sprintf("du -sb %s 2>/dev/null | cut -f1 || echo 0", cacheDir)
}

// LogPath returns the remote path the onstart script tees server output to.
func (e *LlamaCppEngine) LogPath() string {
	return logPath
}

// procKill builds a /proc scan that signals every llama-server process. Uses no
// procps tooling because the image doesn't ship any.
func procKill(signal string) string {
	sig := ""
	if signal != "" {
		sig = signal + " "
	}
	return fmt.Sprintf("for p in /proc/[0-9]*; do grep -qs '%s' $p/cmdline && kill %s${p#/proc/} 2>/dev/null; done",
		procPattern, sig)
}

// buildScript writes the startup script executed on the instance. It resolves the
// binary and cd's next to it: the CUDA build uses GGML_BACKEND_DL, so the ggml
// backend .so files sit alongside llama-server and are loaded at runtime.
//
// No trailing newline — BuildOnstart writes this via `echo`, which appends one.
func (e *LlamaCppEngine) buildScript(model *entity.Model, numGPUs, contextLength int) string {
	return fmt.Sprintf(`set -e
BIN=%s
if command -v llama-server >/dev/null 2>&1; then BIN=$(command -v llama-server); fi
cd "$(dirname "$BIN")"
exec "$BIN" %s`, binaryPath, strings.Join(e.serveArgs(model, numGPUs, contextLength), " "))
}

// serveArgs assembles the llama-server argument list. Engine-owned flags come
// first and are stripped from the catalog args, so the offer's GPU count, the
// scaled context, the port the tunnel expects and the id reported by /v1/models
// have exactly one source of truth.
func (e *LlamaCppEngine) serveArgs(model *entity.Model, numGPUs, contextLength int) []string {
	if numGPUs <= 0 {
		numGPUs = 1
	}
	catalog := stripFlagPair(model.LlamaArgs, engineOwnedFlags...)

	args := []string{
		"-hf", modelRef(model),
		// --alias sets the id reported by /v1/models, which is what opencode
		// sends back as `model`. Use the catalog name so config keys stay short
		// and stable across quant changes.
		"--alias", model.Name,
		"--host", "0.0.0.0",
		"--port", fmt.Sprintf("%d", serverPort),
		// Offload every layer; llama.cpp clamps this to the real layer count.
		"-ngl", "999",
	}
	if contextLength > 0 {
		args = append(args, "--ctx-size", fmt.Sprintf("%d", contextLength))
	}
	// Multi-GPU: pipeline the layers across cards. This is llama.cpp's default,
	// stated explicitly because `tensor` split is the alternative and we never
	// want it silently picked up. Skipped if the catalog chose a mode itself.
	if numGPUs > 1 && !hasAnyFlag(catalog, "-sm", "--split-mode") {
		args = append(args, "--split-mode", "layer")
	}
	return append(args, catalog...)
}

// modelRef renders the `-hf <user>/<repo>[:quant]` reference. llama.cpp matches
// the quant tag case-insensitively against the GGUF filenames in the repo and
// falls back to Q4_K_M when no tag is given.
func modelRef(model *entity.Model) string {
	if model.Quant == "" {
		return model.HFRepo
	}
	return model.HFRepo + ":" + model.Quant
}

// engineOwnedFlags are stripped from Model.LlamaArgs — all of them take a value
// and all are injected from runtime facts. Short and long spellings both listed
// because llama.cpp accepts either.
var engineOwnedFlags = []string{
	"-hf", "-hfr", "--hf-repo",
	"-hff", "--hf-file",
	"-m", "--model",
	"--host", "--port",
	"-a", "--alias",
	"-c", "--ctx-size",
	"-ngl", "--n-gpu-layers", "--gpu-layers",
}

// stripFlagPair removes "--flag value" pairs matching any of the given flag names.
func stripFlagPair(args []string, flags ...string) []string {
	drop := make(map[string]bool, len(flags))
	for _, f := range flags {
		drop[f] = true
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if drop[args[i]] && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func hasAnyFlag(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}
