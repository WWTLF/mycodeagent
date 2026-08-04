package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

const (
	// Verified against the registry: "cuda12" is not a tag vastai/pytorch publishes
	// (1145 tags, none by that name), so this 404'd on every deploy. Pinned to a
	// dated build for reproducibility.
	jupyterImage      = "vastai/pytorch:2.12.0-cuda-12.6.3-24.04-2026-06-15"
	jupyterServerPort = 8888

	// Neither path contains the literal "jupyter". That is not cosmetic: the
	// liveness pattern below matches any /proc entry containing the process
	// name, and a script called start_jupyter.sh puts that name into the argv of
	// the shell running it. The probe then reports ALIVE forever, and the kill
	// sweep signals its own SSH session. Same class of bug as llama.cpp's
	// bracketed pattern, arriving through the filename instead of the pattern.
	jupyterScriptPath = "/tmp/start_lab.sh"
	jupyterLogPath    = "/tmp/lab.log"

	// jupyterWorkDir is the directory JupyterLab serves as its file-browser root,
	// the one the script chdir's into before launching, and the one SyncDirs
	// pulls back.
	//
	// The chdir is not redundant with --ServerApp.root_dir. root_dir only moves
	// the file browser; the server process keeps the shell's directory, /root,
	// and a kernel that resolves its cwd from the process rather than from the
	// notebook's path inherits it. Relative paths in a notebook then point at
	// /root: `Path("data/books").glob("*.txt")` yields nothing, and a corpus
	// loader reading it produces an empty corpus with no error at all — the
	// failure only surfaces much later, as a KeyError deep inside a tokenizer.
	// Observed on a live instance: 35 files present under /workspace/data/books,
	// the notebook counted 0 tokens.
	//
	// It has to be set explicitly. Left alone, JupyterLab serves its working
	// directory, which under `runtype: "ssh"` is /root — so every notebook the
	// user created landed there while the sync watched /workspace, which the image
	// does not create. The resolver found no candidate, the loop copied nothing,
	// and `kill` destroyed the work the sync exists to preserve. Verified on a live
	// deploy: the lab process reported cwd -> /root and none of the candidates
	// existed.
	//
	// Syncing /root instead would be the wrong repair — it holds .cache, .ssh and
	// the pip/conda trees, so the loop would haul gigabytes of image content back
	// on every pass. A dedicated directory keeps the transfer to the user's files.
	jupyterWorkDir = "/workspace"

	// jupyterProcPattern matches the JupyterLab process in /proc/<pid>/cmdline.
	// Bracketed like llama.cpp's, so the pattern text cannot match the shell
	// that carries it: the regex needs a literal "jupyter", and the command
	// string spells it "jupyte[r]".
	jupyterProcPattern = "jupyte[r]"
)

// JupyterEngine implements service.EngineProvider for Jupyter+PyTorch.
//
// Uses the vast.ai recommended PyTorch image which is pre-cached on most hosts.
// PyTorch is pre-installed at /venv/main/. Jupyter is started with no auth
// token: the SSH tunnel is the access control, exactly as for llama-server.
type JupyterEngine struct{}

func NewJupyterEngine() *JupyterEngine {
	return &JupyterEngine{}
}

func (e *JupyterEngine) DockerImage(model *entity.Model) string {
	return jupyterImage
}

func (e *JupyterEngine) EnvVars(model *entity.Model) map[string]string {
	// vast.ai's PyTorch image reads these to decide whether to bring up its own
	// Jupyter and whether to demand a password. Set explicitly so the image's
	// defaults cannot reintroduce a login prompt behind the tunnel.
	return map[string]string{
		"JUPYTER_PASSWORD": "",
		"OPEN_BUTTON_PORT": fmt.Sprintf("%d", jupyterServerPort),
	}
}

// BuildOnstart writes and runs the startup script. vast.ai replaces the image
// ENTRYPOINT for runtype "ssh", so nothing starts on its own — the script has to
// launch JupyterLab itself.
//
// The "already running?" test is an HTTP probe, not a /proc grep. A grep here
// matched the script's own command line and concluded Jupyter was up before it
// had ever been started, so the server never launched at all. Asking the port
// cannot lie about it.
func (e *JupyterEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	script := fmt.Sprintf(`set -e
echo "Jupyter+PyTorch instance starting (GPUs: %d)" > %s
if command -v nvidia-smi >/dev/null 2>&1; then nvidia-smi >> %s 2>&1 || true; fi

mkdir -p %s
cd %s

if curl -sf http://localhost:%d/ >/dev/null 2>&1; then
    echo "JupyterLab already serving on %d" >> %s
else
    echo "Starting JupyterLab..." >> %s
    [ -f /venv/main/bin/activate ] && . /venv/main/bin/activate
    BIN=$(command -v jupyter || echo /venv/main/bin/jupyter)
    if [ ! -x "$BIN" ]; then
        echo "FATAL: no jupyter binary on this image" >> %s
        exit 1
    fi
    nohup "$BIN" lab --ip=0.0.0.0 --port=%d --no-browser --allow-root \
        --ServerApp.token='' --ServerApp.password='' --ServerApp.allow_origin='*' \
        --ServerApp.root_dir=%s \
        >> %s 2>&1 &
fi

for i in $(seq 1 60); do
    if curl -sf http://localhost:%d/ >/dev/null 2>&1; then
        echo "JupyterLab is ready on %d" >> %s
        exit 0
    fi
    sleep 2
done
echo "FATAL: JupyterLab did not answer on %d within 120s" >> %s
exit 1`,
		numGPUs, jupyterLogPath, jupyterLogPath,
		jupyterWorkDir, jupyterWorkDir,
		jupyterServerPort, jupyterServerPort, jupyterLogPath,
		jupyterLogPath, jupyterLogPath,
		jupyterServerPort, jupyterWorkDir, jupyterLogPath,
		jupyterServerPort, jupyterServerPort, jupyterLogPath,
		jupyterServerPort, jupyterLogPath)

	escaped := strings.ReplaceAll(script, "'", `'\''`)
	return fmt.Sprintf("echo '%s' > %s && chmod +x %s && bash %s 2>&1 | tee -a %s",
		escaped, jupyterScriptPath, jupyterScriptPath, jupyterScriptPath, jupyterLogPath)
}

func (e *JupyterEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	return fmt.Sprintf("jupyter lab --ip=0.0.0.0 --port=%d --no-browser --allow-root --ServerApp.token=''",
		jupyterServerPort)
}

func (e *JupyterEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = strings.Join([]string{
		procKillPattern(jupyterProcPattern, ""),
		"sleep 3",
		procKillPattern(jupyterProcPattern, "-9"),
		"sleep 1",
	}, "; ")
	startCmd = fmt.Sprintf("nohup bash %s 2>&1 | tee -a %s &", jupyterScriptPath, jupyterLogPath)
	return
}

func (e *JupyterEngine) LivenessCommand(model *entity.Model) string {
	return livenessProbe(jupyterProcPattern)
}

func (e *JupyterEngine) DownloadedBytesCommand(model *entity.Model) string {
	// No download phase: the image carries everything and the user's notebooks
	// pull their own data. The reporter stays silent on a zero.
	return "echo 0"
}

func (e *JupyterEngine) LogPath(model *entity.Model) string {
	return jupyterLogPath
}

func (e *JupyterEngine) ServerPort(model *entity.Model) int {
	if model != nil && model.ServerPort > 0 {
		return model.ServerPort
	}
	return jupyterServerPort
}

func (e *JupyterEngine) HealthPath(model *entity.Model) string {
	if model != nil && model.HealthPath != "" {
		return model.HealthPath
	}
	return "/"
}

// SyncDirs pulls back the notebook workspace — the whole point of the engine.
//
// Two-way, unlike ComfyUI's outputs. Notebooks are source files the operator
// edits, and an instance is disposable: a pull-only sync would rescue the last
// session's work and then have no way to put it back on the next machine, so
// every deploy would start empty with the previous notebooks sitting locally.
// Push seeds the fresh instance first, then the pull brings changes back.
//
// jupyterWorkDir leads the candidate list and is also what BuildOnstart hands
// --ServerApp.root_dir, so the directory the lab writes to and the directory
// this reads are the same constant. The remaining candidates cover images that
// place a workspace elsewhere.
func (e *JupyterEngine) SyncDirs(model *entity.Model) []entity.SyncDir {
	return []entity.SyncDir{
		{
			RemoteCandidates: []string{jupyterWorkDir, "/root/workspace", "/notebooks"},
			Local:            "workspace",
			Description:      "notebooks and data",
			Push:             true,
			// No RootMarker: the push-creation branch it drives is for a leaf that
			// may never appear on its own (ComfyUI's workflows/). Here the onstart
			// script mkdir's the root before the lab starts, so the plain
			// candidate test always finds it. A marker would also have nowhere to
			// search — the resolver walks up with dirname, and from /workspace the
			// first step is already /.
		},
	}
}
