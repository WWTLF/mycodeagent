package engine

import (
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

// MultiEngine wraps multiple engine implementations and routes calls based on
// the model's EngineType. This is the single service.EngineProvider that
// DeployService depends on — no layering violation.
type MultiEngine struct {
	llamaCpp *LlamaCppEngine
	comfyUI  *ComfyUIEngine
	jupyter  *JupyterEngine
}

func NewMultiEngine() *MultiEngine {
	return &MultiEngine{
		llamaCpp: NewLlamaCppEngine(),
		comfyUI:  NewComfyUIEngine(),
		jupyter:  NewJupyterEngine(),
	}
}

func (m *MultiEngine) engine(model *entity.Model) EngineProvider {
	switch model.EngineType {
	case entity.EngineComfyUI:
		return m.comfyUI
	case entity.EngineJupyter:
		return m.jupyter
	default:
		return m.llamaCpp
	}
}

func (m *MultiEngine) DockerImage(model *entity.Model) string {
	return m.engine(model).DockerImage(model)
}

func (m *MultiEngine) BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string {
	return m.engine(model).BuildOnstart(model, numGPUs, contextLength, hfToken)
}

func (m *MultiEngine) BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string {
	return m.engine(model).BuildRawCommand(model, numGPUs, contextLength)
}

func (m *MultiEngine) RestartCommands(model *entity.Model) (killCmd string, startCmd string) {
	return m.engine(model).RestartCommands(model)
}

func (m *MultiEngine) LivenessCommand(model *entity.Model) string {
	return m.engine(model).LivenessCommand(model)
}

func (m *MultiEngine) DownloadedBytesCommand(model *entity.Model) string {
	return m.engine(model).DownloadedBytesCommand(model)
}

func (m *MultiEngine) LogPath(model *entity.Model) string {
	return m.engine(model).LogPath(model)
}

func (m *MultiEngine) ServerPort(model *entity.Model) int {
	return m.engine(model).ServerPort(model)
}

func (m *MultiEngine) HealthPath(model *entity.Model) string {
	return m.engine(model).HealthPath(model)
}

func (m *MultiEngine) EnvVars(model *entity.Model) map[string]string {
	return m.engine(model).EnvVars(model)
}

// EngineProvider is the common interface for all engine implementations.
// Kept package-private — the domain layer uses service.EngineProvider.
type EngineProvider interface {
	DockerImage(model *entity.Model) string
	BuildOnstart(model *entity.Model, numGPUs, contextLength int, hfToken string) string
	BuildRawCommand(model *entity.Model, numGPUs, contextLength int) string
	RestartCommands(model *entity.Model) (killCmd string, startCmd string)
	LivenessCommand(model *entity.Model) string
	DownloadedBytesCommand(model *entity.Model) string
	LogPath(model *entity.Model) string
	ServerPort(model *entity.Model) int
	HealthPath(model *entity.Model) string
	EnvVars(model *entity.Model) map[string]string
}
