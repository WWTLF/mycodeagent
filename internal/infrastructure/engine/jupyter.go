package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

const (
	jupyterImage      = "vastai/pytorch:cuda12"
	jupyterServerPort = 8888

	// Neither path contains the literal "jupyter". That is not cosmetic: the
	// liveness pattern below matches any /proc entry containing the process
	// name, and a script called start_jupyter.sh puts that name into the argv of
	// the shell running it. The probe then reports ALIVE forever, and the kill
	// sweep signals its own SSH session. Same class of bug as llama.cpp's
	// bracketed pattern, arriving through the filename instead of the pattern.
	jupyterScriptPath = "/tmp/start_lab.sh"
	jupyterLogPath    = "/tmp/lab.log"

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
		jupyterServerPort, jupyterServerPort, jupyterLogPath,
		jupyterLogPath, jupyterLogPath,
		jupyterServerPort, jupyterLogPath,
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
