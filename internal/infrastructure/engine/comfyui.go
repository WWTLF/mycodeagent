package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

const (
	// Verified against the registry: ai-dock/comfyui has no 24.04 and no 12.8 tag at
	// all — the previous value was invented by analogy with the llama.cpp image and
	// 404s, so every `init comfyui` created a billing instance that then failed to
	// pull. Pinned rather than :latest-cuda so a silent upstream change cannot
	// break a deploy. CUDA 12.1.1 runs fine on the cuda_max_good >= 12.8 hosts the
	// offer search selects; a newer driver runs an older runtime.
	comfyUIImage      = "ghcr.io/ai-dock/comfyui:v2-cuda-12.1.1-base-22.04-v0.2.7"
	comfyUIServerPort = 8188
	comfyUIScriptPath = "/tmp/start_cui.sh"
	comfyUILogPath    = "/tmp/cui.log"

	// comfyUIProcPattern matches ComfyUI's entry script in /proc/<pid>/cmdline.
	// Bracketed for the same reason as llama.cpp's and Jupyter's: the pattern
	// must not match the shell carrying it. "mai[n]" matches "main", the command
	// text spells it "mai[n]".
	comfyUIProcPattern = `mai[n]\.py`
)

// ComfyUIEngine implements service.EngineProvider for ComfyUI.
//
// Uses the ai-dock image, which is built for GPU cloud environments including
// vast.ai and ships ComfyUI plus ComfyUI-Manager.
type ComfyUIEngine struct{}

func NewComfyUIEngine() *ComfyUIEngine {
	return &ComfyUIEngine{}
}

func (e *ComfyUIEngine) DockerImage(model *entity.Model) string {
	return comfyUIImage
}

// EnvVars disables the image's own HTTP auth. The SSH tunnel is the access
// control — the port is never exposed publicly — and a login prompt on a
// single-user tunnel is just a way to lock yourself out.
//
// These have to be real environment variables on the instance. They were
// previously only described in a comment here, which set nothing at all.
func (e *ComfyUIEngine) EnvVars(model *entity.Model) map[string]string {
	return map[string]string{
		"WEB_ENABLE_AUTH": "false",
		"USERNAME":        "",
		"PASSWORD":        "",
	}
}

// BuildOnstart writes and runs the startup script.
//
// It launches ComfyUI itself rather than relying on the image ENTRYPOINT:
// instances are created with runtype "ssh", and vast.ai replaces the entrypoint
// in that mode. The previous version only polled /history and waited for a
// supervisord that never ran, then exited 0 either way — so a container that
// started nothing was indistinguishable from a healthy one.
//
// supervisord is still preferred when present, because the ai-dock image wires
// its own service management; the direct launch is the fallback. The exact
// install path is resolved at runtime instead of assumed, the same approach
// llama.cpp's script uses for its binary.
func (e *ComfyUIEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	script := fmt.Sprintf(`set -e
echo "ComfyUI instance starting (GPUs: %d)" > %s
if command -v nvidia-smi >/dev/null 2>&1; then nvidia-smi >> %s 2>&1 || true; fi

if command -v supervisord >/dev/null 2>&1 && [ -f /etc/supervisor/supervisord.conf ]; then
    echo "Starting via supervisord" >> %s
    nohup supervisord -c /etc/supervisor/supervisord.conf >> %s 2>&1 &
else
    DIR=""
    for d in /opt/ComfyUI /opt/comfyui /workspace/ComfyUI /ComfyUI /root/ComfyUI; do
        if [ -f "$d/main.py" ]; then DIR="$d"; break; fi
    done
    if [ -z "$DIR" ]; then
        echo "FATAL: ComfyUI main.py not found in any known location" >> %s
        exit 1
    fi
    echo "Starting ComfyUI from $DIR" >> %s
    PY=$(command -v python3 || command -v python)
    cd "$DIR"
    nohup "$PY" main.py --listen 0.0.0.0 --port %d >> %s 2>&1 &
fi

for i in $(seq 1 150); do
    if curl -sf http://localhost:%d/history >/dev/null 2>&1; then
        echo "ComfyUI is ready on %d" >> %s
        exit 0
    fi
    sleep 2
done
echo "FATAL: ComfyUI did not answer on %d within 300s" >> %s
exit 1`,
		numGPUs, comfyUILogPath, comfyUILogPath,
		comfyUILogPath, comfyUILogPath,
		comfyUILogPath, comfyUILogPath,
		comfyUIServerPort, comfyUILogPath,
		comfyUIServerPort, comfyUIServerPort, comfyUILogPath,
		comfyUIServerPort, comfyUILogPath)

	escaped := strings.ReplaceAll(script, "'", `'\''`)
	return fmt.Sprintf("echo '%s' > %s && chmod +x %s && bash %s 2>&1 | tee -a %s",
		escaped, comfyUIScriptPath, comfyUIScriptPath, comfyUIScriptPath, comfyUILogPath)
}

func (e *ComfyUIEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	return fmt.Sprintf("python main.py --listen 0.0.0.0 --port %d", comfyUIServerPort)
}

func (e *ComfyUIEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	killCmd = strings.Join([]string{
		procKillPattern(comfyUIProcPattern, ""),
		"sleep 3",
		procKillPattern(comfyUIProcPattern, "-9"),
		"sleep 1",
	}, "; ")
	startCmd = fmt.Sprintf("nohup bash %s 2>&1 | tee -a %s &", comfyUIScriptPath, comfyUILogPath)
	return
}

func (e *ComfyUIEngine) LivenessCommand(model *entity.Model) string {
	return livenessProbe(comfyUIProcPattern)
}

func (e *ComfyUIEngine) DownloadedBytesCommand(model *entity.Model) string {
	// No download phase: checkpoints are pulled on demand through the UI, so
	// there is no total to report progress against. The reporter ignores a zero.
	return "echo 0"
}

func (e *ComfyUIEngine) LogPath(model *entity.Model) string {
	return comfyUILogPath
}

func (e *ComfyUIEngine) ServerPort(model *entity.Model) int {
	if model != nil && model.ServerPort > 0 {
		return model.ServerPort
	}
	return comfyUIServerPort
}

func (e *ComfyUIEngine) HealthPath(model *entity.Model) string {
	if model != nil && model.HealthPath != "" {
		return model.HealthPath
	}
	return "/history"
}

// SyncDirs lists what ComfyUI produces and therefore what must survive `kill`.
//
// Paths are candidates because the layout is not fixed: ai-dock ships ComfyUI
// under /opt/ComfyUI and syncs it to $WORKSPACE (its OPT_SYNC=ComfyUI env), so
// the live tree may be either. The same uncertainty is why BuildOnstart probes
// five locations for main.py.
//
// models/ is deliberately absent. Checkpoints are tens of gigabytes and were
// downloaded from the internet, not produced here — copying them back over a
// home connection would cost far more than re-fetching them.
func (e *ComfyUIEngine) SyncDirs(model *entity.Model) []entity.SyncDir {
	return []entity.SyncDir{
		{
			RemoteCandidates: []string{
				"/opt/ComfyUI/output",
				"/workspace/ComfyUI/output",
				"/ComfyUI/output",
				"/root/ComfyUI/output",
			},
			Local:       "output",
			Description: "generated images",
		},
		{
			RemoteCandidates: []string{
				"/opt/ComfyUI/user/default/workflows",
				"/workspace/ComfyUI/user/default/workflows",
				"/ComfyUI/user/default/workflows",
				"/root/ComfyUI/user/default/workflows",
			},
			Local:       "workflows",
			Description: "saved workflows",
		},
	}
}
