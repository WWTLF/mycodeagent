package engine

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

const (
	// vast.ai's own ComfyUI image, and the reason this is not ai-dock's any more.
	//
	// ai-dock/comfyui tops out at ComfyUI v0.2.7 — November 2024. Its newest tag
	// is a year and a half stale, which shipped a frontend so old that the Queue
	// Prompt control still lived in the floating panel the modern UI replaced.
	// The registry confirms the abandonment: the only ComfyUI versions ai-dock
	// publishes are 0.0.8, 0.2.0, 0.2.2, 0.2.6 and 0.2.7.
	//
	// vastai/comfy is rebuilt continuously (v0.29.2 was published the day this
	// was pinned) and is the image vast.ai's own ComfyUI template uses, so it is
	// the one most likely to be pre-cached on a host — which is time off the
	// provisioning phase that dominates our startup budget.
	//
	// The tag says cuda-12.9, but what matters is the torch wheel: the image
	// builds with PYTORCH_BACKEND=cu128, so the driver floor is 12.8 — exactly
	// the cuda_max_good filter the offer search already applies. Choosing the
	// cuda-13.2 variant instead would need a 13.x driver and would shrink the
	// offer pool for no benefit.
	//
	// Pinned to an exact version rather than a floating tag so an upstream push
	// cannot silently change what deploys.
	comfyUIImage      = "vastai/comfy:v0.29.2-cuda-12.9-py312"
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
// Uses vast.ai's own image, which ships ComfyUI, ComfyUI-Manager, xformers and
// sageattention in a virtualenv at /venv/main, plus an SD1.5 checkpoint already
// on disk — so a deploy with no provisioning script still opens on something
// that can generate.
type ComfyUIEngine struct{}

func NewComfyUIEngine() *ComfyUIEngine {
	return &ComfyUIEngine{}
}

func (e *ComfyUIEngine) DockerImage(model *entity.Model) string {
	return comfyUIImage
}

// EnvVars is deliberately empty.
//
// It used to set WEB_ENABLE_AUTH=false, USERNAME and PASSWORD to switch off the
// ai-dock image's HTTP auth. Those names mean nothing to vastai/comfy: its auth
// lives in the caddy/portal stack that its ENTRYPOINT starts, and runtype "ssh"
// replaces the ENTRYPOINT — so BuildOnstart runs main.py itself and no auth
// layer exists to disable. Carrying the variables forward would be dead config
// that reads like a live safeguard.
//
// Access control is the SSH tunnel: the image declares no ExposedPorts, so
// nothing reaches the server except through the forward.
func (e *ComfyUIEngine) EnvVars(model *entity.Model) map[string]string {
	return nil
}

// BuildOnstart writes and runs the startup script.
//
// The script does everything itself and relies on nothing the image would
// normally do at boot. Under runtype "ssh" vast.ai replaces the ENTRYPOINT, so
// the image's boot sequence never runs — /opt/instance-tools/bin/entrypoint.sh
// for vastai/comfy, /opt/ai-dock/bin/init.sh for the ai-dock image before it.
// That script is what would set WORKSPACE, fetch PROVISIONING_SCRIPT and start
// the services. Two failures observed on a live deploy follow directly from
// assuming otherwise:
//
//   - Preferring supervisord killed the deploy outright. Its caddy unit
//     interpolates %(ENV_WORKSPACE)s, WORKSPACE is set by the init script that
//     never ran, and supervisord refuses to start on an unexpandable name. The
//     direct-launch branch then never executed, so nothing listened on 8188 and
//     the deploy burned its whole budget. supervisord is gone from this script:
//     under this runtype it cannot work.
//
//   - PROVISIONING_SCRIPT was delivered to the container and read by nobody.
//     init.sh is its only consumer, so `--provisioning` silently did nothing.
//     The script is now fetched and run here, before ComfyUI starts, which is
//     the ordering models need.
//
// The interpreter is resolved *before* provisioning runs, and exported as
// COMFYUI_PYTHON. Provisioning scripts install custom nodes, and a custom node
// with a requirements.txt has to install into the same virtualenv ComfyUI will
// import from — with the resolution after provisioning, a script had no way to
// name that interpreter and could only guess at `python3`, which is the very
// mistake this script exists to avoid.
func (e *ComfyUIEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	script := fmt.Sprintf(`set -e
LOG=%s
echo "ComfyUI instance starting (GPUs: %d)" > "$LOG"
if command -v nvidia-smi >/dev/null 2>&1; then nvidia-smi >> "$LOG" 2>&1 || true; fi

# WORKSPACE is normally exported by the image's boot sequence, which this
# runtype prevents from running. Provisioning scripts write models relative to
# it.
export WORKSPACE="${WORKSPACE:-/workspace}"
mkdir -p "$WORKSPACE"
echo "WORKSPACE=$WORKSPACE" >> "$LOG"

# /opt/workspace-internal/ComfyUI is where vastai/comfy clones it; the rest are
# kept so a change of image does not silently become a failed deploy.
DIR=""
for d in /opt/workspace-internal/ComfyUI /opt/ComfyUI /opt/comfyui \
         "$WORKSPACE/ComfyUI" /ComfyUI /root/ComfyUI; do
    if [ -f "$d/main.py" ]; then DIR="$d"; break; fi
done
if [ -z "$DIR" ]; then
    echo "FATAL: ComfyUI main.py not found in any known location" >> "$LOG"
    exit 1
fi
export COMFYUI_DIR="$DIR"
echo "ComfyUI at $DIR" >> "$LOG"

# The interpreter must be the image's own venv, not the system python. Every
# ComfyUI image installs torch into a virtualenv only: ai-dock advertised the
# path in COMFYUI_VENV_PYTHON, vastai/comfy puts it at /venv/main. Resolving
# with command -v python3 found /usr/bin/python3 and ComfyUI died on
# "No module named 'torch'" every time.
PY=""
for cand in "$COMFYUI_VENV_PYTHON" "$COMFYUI_VENV/bin/python" \
            /venv/main/bin/python \
            /opt/environments/python/comfyui/bin/python \
            "$DIR/venv/bin/python"; do
    if [ -n "$cand" ] && [ -x "$cand" ] && "$cand" -c "import torch" >/dev/null 2>&1; then
        PY="$cand"; break
    fi
done
if [ -z "$PY" ]; then
    # Last resort: whatever python can import torch at all.
    for cand in $(command -v python3) $(command -v python); do
        if [ -n "$cand" ] && "$cand" -c "import torch" >/dev/null 2>&1; then PY="$cand"; break; fi
    done
fi
if [ -z "$PY" ]; then
    echo "FATAL: no python with torch found (checked COMFYUI_VENV_PYTHON, $COMFYUI_VENV, system)" >> "$LOG"
    exit 1
fi
export COMFYUI_PYTHON="$PY"
echo "python: $PY" >> "$LOG"

# Provisioning runs before the server so models are present when it opens.
# A failure here is not fatal: an empty ComfyUI you can add models to beats no
# ComfyUI at all, and the log says which happened.
if [ -n "$PROVISIONING_SCRIPT" ]; then
    echo "Provisioning from $PROVISIONING_SCRIPT" >> "$LOG"
    if curl -fsSL -o /tmp/provisioning.sh "$PROVISIONING_SCRIPT"; then
        chmod +x /tmp/provisioning.sh
        bash /tmp/provisioning.sh >> "$LOG" 2>&1 \
            && echo "Provisioning finished" >> "$LOG" \
            || echo "WARNING: provisioning script failed; continuing without it" >> "$LOG"
    else
        echo "WARNING: could not fetch provisioning script; continuing" >> "$LOG"
    fi
fi

echo "Starting ComfyUI from $DIR using $PY" >> "$LOG"
cd "$DIR"
nohup "$PY" main.py --listen 0.0.0.0 --port %d >> "$LOG" 2>&1 &

for i in $(seq 1 150); do
    if curl -sf http://localhost:%d/history >/dev/null 2>&1; then
        echo "ComfyUI is ready on %d" >> "$LOG"
        exit 0
    fi
    sleep 2
done
echo "FATAL: ComfyUI did not answer on %d within 300s" >> "$LOG"
exit 1`,
		comfyUILogPath, numGPUs,
		comfyUIServerPort,
		comfyUIServerPort, comfyUIServerPort,
		comfyUIServerPort)

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
// Paths are candidates because the layout is not fixed: vastai/comfy clones
// into /opt/workspace-internal/ComfyUI, ai-dock shipped /opt/ComfyUI and synced
// it to $WORKSPACE, so the live tree may be any of them. The same uncertainty
// is why BuildOnstart probes several locations for main.py.
//
// models/ is deliberately absent. Checkpoints are tens of gigabytes and were
// downloaded from the internet, not produced here — copying them back over a
// home connection would cost far more than re-fetching them.
func (e *ComfyUIEngine) SyncDirs(model *entity.Model) []entity.SyncDir {
	return []entity.SyncDir{
		{
			RemoteCandidates: []string{
				"/opt/workspace-internal/ComfyUI/output",
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
				"/opt/workspace-internal/ComfyUI/user/default/workflows",
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
