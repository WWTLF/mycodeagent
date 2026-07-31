package persistence

import (
	"fmt"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

// All models run on llama.cpp (the only engine), so every entry points at a GGUF
// repository plus a quant tag. Repos, quant tags and file sizes below were verified
// against the HuggingFace API, and every chat template was checked to actually
// declare thinking and tool support before Reasoning/ToolCalling were set here.
//
// The catalog spans four VRAM tiers — 16 / 24 / 32 / 48 GB — and is optimised for
// CAPABILITY first, with speed only required to stay usable for interactive coding.
// That is why the working tiers are DENSE models: a 27B dense uses all 27B
// parameters per token, whereas a 35B-A3B MoE activates only 3B and behaves closer
// to a ~10B model. The MoE is far faster (roughly 60-100 vs 20-30 tok/s) but not as
// strong, so it is kept as one explicit `coder-fast` entry rather than the default.
//
// Sizing, per GPU:
//
//	weights + KV(ctx) + ~1.5 GB compute buffers  <  usable VRAM
//
// Usable VRAM is below nameplate (a "24 GB" 3090 reports ~23.4), and KV per token at
// q8_0 is 2 * n_layers * n_kv_heads * head_dim * ~1.06 bytes:
//
//	Qwen3.5-9B       dense  L=32 kv=4  hd=256  ->   68 KB/token
//	Qwen3.6-27B      dense  L=64 kv=4  hd=256  ->  136 KB/token
//	Qwen3.6-35B-A3B  MoE    L=40 kv=2  hd=256  ->   42 KB/token
//
// The dense 27B pays for its capability in context: 136 KB/token is 3.2x the MoE, so
// the same card holds far less window. That trade is deliberate. See docs/Solution.md.
//
// Quant tags must be UNIQUE substrings of a filename in the repo. "Q6_K" is NOT —
// it also matches "UD-Q6_K_XL" — so the 48 GB tier names the XL tag explicitly.
// Check any new tag against the repo file list before adding it.
//
// --no-mmproj: several of these repos ship a ~0.9 GB vision projector that `-hf`
// would auto-download and offload to VRAM. Everything here is served text-only. To
// enable vision, drop the flag and set Vision: true.
//
// No YaRN anywhere: Qwen3.5 and Qwen3.6 are natively 262144 context.
//
// StartupTimeout budget. One context covers the WHOLE deploy — provisioning,
// SSH, download and weight load all draw on it — so the number has to survive
// the worst case of each, not the typical case:
//
//	timeout = 10 min provisioning + (size / 25 MB/s) + 1 min VRAM load
//
// Both constants are measured, not guessed. Provisioning (offer accepted →
// actual_status "running") took 9.3 minutes on a slow host and seconds on a good
// one; it is the host's image pull, and nothing here can speed it up. 25 MB/s is
// a deliberately pessimistic download rate — 43 MB/s was observed on a healthy
// host, and halving it leaves room for a bad one.
//
// Being generous costs almost nothing: a doomed deploy burns pennies of GPU time
// before the timeout fires, a *dead* server is caught in seconds by the liveness
// watcher regardless, and the instance self-destroys either way. Being stingy
// costs a whole deploy. The original 8-15 min values could not even cover the
// provisioning phase alone on a slow host.
var defaultModels = []*entity.Model{
	// --- Image Generation ---
	{
		// 48 GB ComfyUI. The ai-dock image ships ComfyUI + ComfyUI-Manager. The
		// image's own HTTP auth is disabled through EngineType's EnvVars — the SSH
		// tunnel is the access control. Checkpoints are pulled on demand through
		// the UI, so DownloadGB is 0 and there is no download progress to report.
		//
		// StartupTimeout is dominated by the image, not by any download: the
		// ai-dock image is far larger than llama.cpp's ~2.6 GB compressed, and the
		// provisioning phase alone has been measured at 9.3 minutes on a slow host
		// with the small one. 10 minutes could not cover provisioning by itself.
		Name:           "comfyui",
		Alias:          "comfyui",
		Category:       entity.CategoryImageGen,
		EngineType:     entity.EngineComfyUI,
		VRAM:           48,
		NumGPUs:        1,
		DiskGB:         60,
		DownloadGB:     0,
		StartupTimeout: 25 * time.Minute,
		ServerPort:     8188,
		HealthPath:     "/history",
		Reasoning:      false,
		Vision:         false,
		ToolCalling:    false,
	},
	// --- Data Science ---
	{
		// 32 GB Jupyter + PyTorch. The vast.ai recommended PyTorch image, pre-cached
		// on most hosts — which is why this keeps a shorter budget than ComfyUI.
		// PyTorch is at /venv/main/. JupyterLab starts with no token: the SSH
		// tunnel is the access control, exactly as for llama-server.
		Name:           "jupyter-pytorch",
		Alias:          "jupyter",
		Category:       entity.CategoryDataScience,
		EngineType:     entity.EngineJupyter,
		VRAM:           32,
		NumGPUs:        1,
		DiskGB:         40,
		DownloadGB:     0,
		StartupTimeout: 15 * time.Minute,
		ServerPort:     8888,
		HealthPath:     "/",
		Reasoning:      false,
		Vision:         false,
		ToolCalling:    false,
	},
	// --- Coding (llama.cpp) ---
	{
		// 16 GB. Dense 9B is the largest dense model that fits with real context;
		// a 27B at the 2-bit quant it would need here is worse than a 9B at Q5.
		Name:             "qwen35-9b",
		Alias:            "coder-mini",
		HFRepo:           "unsloth/Qwen3.5-9B-GGUF",
		Quant:            "UD-Q5_K_XL", // 6.7 GB
		Category:         entity.CategoryCoding,
		VRAM:             16,
		NumGPUs:          1,
		DiskGB:           20,
		DownloadGB:       6.7,
		StartupTimeout:   16 * time.Minute,
		ContextLength:    65536, // 6.7 + 4.5 KV + 1.5 = 12.7 of ~15.4
		MaxContextLength: 262144,
		LlamaArgs: []string{
			"--jinja",
			"-fa", "on",
			"--cache-type-k", "q8_0",
			"--cache-type-v", "q8_0",
			"-np", "1",
			"--cache-reuse", "256",
			"--reasoning-format", "deepseek",
			"--no-mmproj",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
	},
	{
		// 24 GB. The capability default for this tier: full 27B active per token.
		// Context is the price — 136 KB/token caps it at 32k here.
		Name:             "qwen36-27b-24g",
		Alias:            "coder",
		HFRepo:           "unsloth/Qwen3.6-27B-GGUF",
		Quant:            "IQ4_XS", // 15.4 GB
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          1,
		DiskGB:           28,
		DownloadGB:       15.4,
		StartupTimeout:   22 * time.Minute,
		ContextLength:    32768, // 15.4 + 4.5 KV + 1.5 = 21.4 of ~23.4
		MaxContextLength: 262144,
		LlamaArgs: []string{
			"--jinja",
			"-fa", "on",
			"--cache-type-k", "q8_0",
			"--cache-type-v", "q8_0",
			"-np", "1",
			"--cache-reuse", "256",
			"--reasoning-format", "deepseek",
			"--no-mmproj",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
	},
	{
		// 24 GB, speed-first alternative. Same card as `coder`, but 3B active
		// instead of 27B: ~3x the tokens/s and 2x the context, at roughly the
		// capability of a 10B dense model. For iterating over large repos.
		Name:             "qwen36-35b-a3b",
		Alias:            "coder-fast",
		HFRepo:           "unsloth/Qwen3.6-35B-A3B-GGUF",
		Quant:            "UD-IQ4_XS", // 17.7 GB
		Category:         entity.CategoryCoding,
		VRAM:             24,
		NumGPUs:          1,
		DiskGB:           32,
		DownloadGB:       17.7,
		StartupTimeout:   24 * time.Minute,
		ContextLength:    65536, // 17.7 + 2.8 KV + 1.5 = 22.0 of ~23.4
		MaxContextLength: 262144,
		LlamaArgs: []string{
			"--jinja",
			"-fa", "on",
			"--cache-type-k", "q8_0",
			"--cache-type-v", "q8_0",
			"-np", "1",
			"--cache-reuse", "256",
			"--reasoning-format", "deepseek",
			"--no-mmproj",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
	},
	{
		// 32 GB. Same 27B, a full quant step up (IQ4_XS -> Q5_K_M) and double the
		// context. Q5_K_M is chosen over Q6_K because "Q6_K" is an ambiguous tag.
		Name:             "qwen36-27b-32g",
		Alias:            "coder-hq",
		HFRepo:           "unsloth/Qwen3.6-27B-GGUF",
		Quant:            "Q5_K_M", // 19.5 GB
		Category:         entity.CategoryCoding,
		VRAM:             32,
		NumGPUs:          1,
		DiskGB:           32,
		DownloadGB:       19.5,
		StartupTimeout:   26 * time.Minute,
		ContextLength:    65536, // 19.5 + 8.9 KV + 1.5 = 29.9 of ~31.4
		MaxContextLength: 262144,
		LlamaArgs: []string{
			"--jinja",
			"-fa", "on",
			"--cache-type-k", "q8_0",
			"--cache-type-v", "q8_0",
			"-np", "1",
			"--cache-reuse", "256",
			"--reasoning-format", "deepseek",
			"--no-mmproj",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
	},
	{
		// 48 GB flagship. ~6.5 bpw is effectively lossless, so this is the 27B at
		// full strength with a 128k window. Passed over here: Qwen3.5-122B-A10B at
		// UD-IQ2_M (39.1 GB, ~35B-equivalent) — bigger on paper, but 2-bit
		// degradation is real and it is a generation behind Qwen3.6.
		Name:             "qwen36-27b-48g",
		Alias:            "coder-max",
		HFRepo:           "unsloth/Qwen3.6-27B-GGUF",
		Quant:            "UD-Q6_K_XL", // 25.6 GB
		Category:         entity.CategoryCoding,
		VRAM:             48,
		NumGPUs:          1,
		DiskGB:           38,
		DownloadGB:       25.6,
		StartupTimeout:   30 * time.Minute,
		ContextLength:    131072, // 25.6 + 17.8 KV + 1.5 = 44.9 of ~47.4
		MaxContextLength: 262144,
		LlamaArgs: []string{
			"--jinja",
			"-fa", "on",
			"--cache-type-k", "q8_0",
			"--cache-type-v", "q8_0",
			"-np", "1",
			"--cache-reuse", "256",
			"--reasoning-format", "deepseek",
			"--no-mmproj",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
	},
	{
		// 32 GB uncensored. Abliterated Qwen3.6-35B-A3B — MoE, so the light KV buys
		// a 128k window here. NOTE: Reasoning/ToolCalling are inherited from the base
		// model's template; the abliterated GGUF's own template was not verified.
		Name:             "qwen36-35b-a3b-abliterated",
		Alias:            "rude",
		HFRepo:           "mradermacher/Huihui-Qwen3.6-35B-A3B-abliterated-i1-GGUF",
		Quant:            "Q4_K_M", // 21.2 GB (matches the i1-Q4_K_M file)
		Category:         entity.CategoryCoding,
		VRAM:             32,
		NumGPUs:          1,
		DiskGB:           34,
		DownloadGB:       21.2,
		StartupTimeout:   26 * time.Minute,
		ContextLength:    131072, // 21.2 + 5.5 KV + 1.5 = 28.2 of ~31.4
		MaxContextLength: 262144,
		LlamaArgs: []string{
			"--jinja",
			"-fa", "on",
			"--cache-type-k", "q8_0",
			"--cache-type-v", "q8_0",
			"-np", "1",
			"--cache-reuse", "256",
			"--reasoning-format", "deepseek",
			"--no-mmproj",
		},
		Reasoning:   true,
		Vision:      false,
		ToolCalling: true,
	},
}

type StaticModelRepository struct {
	models []*entity.Model
}

var _ repository.ModelRepository = (*StaticModelRepository)(nil)

func NewStaticModelRepository() *StaticModelRepository {
	return &StaticModelRepository{models: defaultModels}
}

func (r *StaticModelRepository) FindByName(name string) (*entity.Model, error) {
	for _, m := range r.models {
		if m.Name == name || m.Alias == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", name)
}

func (r *StaticModelRepository) FindByAlias(alias string) (*entity.Model, error) {
	for _, m := range r.models {
		if m.Alias == alias {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", alias)
}

func (r *StaticModelRepository) FindAll() ([]*entity.Model, error) {
	return r.models, nil
}

func (r *StaticModelRepository) FindByCategory(category entity.ModelCategory) ([]*entity.Model, error) {
	var result []*entity.Model
	for _, m := range r.models {
		if m.Category == category {
			result = append(result, m)
		}
	}
	return result, nil
}
