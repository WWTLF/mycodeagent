package entity

import "time"

type ModelCategory string

const (
	CategoryCoding      ModelCategory = "coding"
	CategoryImageGen    ModelCategory = "imagegen"
	CategoryDataScience ModelCategory = "datascience"
)

type EngineType string

const (
	EngineLlamaCpp EngineType = "llamacpp"
	EngineComfyUI  EngineType = "comfyui"
	EngineJupyter  EngineType = "jupyter"
)

type Model struct {
	Name             string
	Alias            string
	HFRepo           string // HuggingFace GGUF repository, e.g. "unsloth/Qwen3-8B-GGUF"
	Quant            string // quant tag inside HFRepo, e.g. "UD-Q4_K_XL" (empty = llama.cpp picks Q4_K_M)
	Category         ModelCategory
	EngineType       EngineType    // which engine handles this model (empty = "llamacpp")
	VRAM             int           // GB required per GPU (baseline)
	NumGPUs          int           // GPU count to rent; SearchOffers matches it with `eq`, so the offer always has exactly this many
	DiskGB           int           // container disk to request on vast.ai; sized to hold the image + GGUF download + scratch (0 = default)
	DownloadGB       float64       // size of the GGUF being fetched; used only to turn download progress into a percentage (0 = report bytes without a total)
	StartupTimeout   time.Duration // max time to wait for instance + model server to become healthy
	ContextLength    int           // baseline context length sized for VRAM (0 = engine default)
	MaxContextLength int           // architectural ceiling; DeployService scales ContextLength up toward this when the offer has more VRAM per GPU than VRAM. 0 = no scaling.
	LlamaArgs        []string      // llama-server arguments; must NOT contain engine-owned flags (-hf, -m, --host, --port, -a, -c, -ngl) — those are injected from runtime values
	Reasoning        bool          // model supports thinking/reasoning
	Vision           bool          // model supports image inputs
	ToolCalling      bool          // model supports tool/function calling
	ServerPort       int           // port the service listens on inside the container (0 = engine default)
	HealthPath       string        // HTTP path for health check ("" = engine default, "skip" = no health check)
}

// SyncDir is one directory kept in step between an instance and the local
// machine.
//
// The whole point: instances are disposable, so anything an engine *produces*
// dies with them. That was free when the only engine was llama.cpp, which reads
// weights and writes nothing. ComfyUI generates images and stores workflows —
// destroying that silently is destroying the user's work.
type SyncDir struct {
	// RemoteCandidates are absolute paths to try, in order. The first that
	// exists on the instance wins.
	RemoteCandidates []string
	// Local is the subdirectory name created under the sync root.
	Local string
	// Description is shown while copying.
	Description string

	// Push also sends local changes up, making the directory two-way.
	//
	// Off by default, because for most of what an engine writes it would be
	// wrong: generated images only ever come *from* the instance, and uploading
	// them back is at best pointless. It is on for directories the operator is
	// expected to edit — workflows, above all, which are source files kept in the
	// working directory and which have to reach the next disposable instance to
	// be worth editing at all.
	//
	// Neither direction ever deletes, and both skip files the receiver has a
	// newer copy of, so the loser of a genuine simultaneous edit is a file that
	// did not move rather than one that was destroyed.
	Push bool

	// RootMarker is a file that identifies the application root somewhere above
	// a candidate path — "main.py" for ComfyUI.
	//
	// Only Push needs it. A pull can wait for the directory to appear, but a push
	// has nowhere to write until it does, and a workflows directory does not
	// exist until the first workflow is saved — which, for someone who edits them
	// locally, may be never. The marker is what makes creating it safe: it
	// distinguishes "this tree is the real install, the leaf is merely absent"
	// from "this candidate belongs to a layout this image does not use", where
	// creating the path would silently sync into somewhere nothing reads.
	RootMarker string
}
