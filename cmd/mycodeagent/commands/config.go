package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/spf13/cobra"
)

// providerPrefix namespaces the provider keys this command owns. Everything
// under it is rewritten on each run; everything outside it is the user's and is
// never touched — that boundary is the whole contract of this command.
const providerPrefix = "mycodeagent-"

func NewConfigCmd(app *application.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Update the userwide opencode config with all running instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			instances, err := app.ListInstances(ctx)
			if err != nil {
				return err
			}
			// Filter to running instances
			var runningInstances []*entity.Instance
			for _, inst := range instances {
				if inst.Status.Is(entity.StatusRunning) || inst.Status.Is(entity.StatusStarting) {
					runningInstances = append(runningInstances, inst)
				}
			}
			instances = runningInstances
			// NOTE: no early return when the list is empty. Bailing out here used
			// to skip the stale-provider cleanup below, so killing an instance and
			// running `config` left a dead `mycodeagent-*` provider pointing at a
			// localhost port nothing listens on — and that port gets handed to the
			// next deploy, so the stale entry then advertises a model the new
			// server doesn't serve.

			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home dir: %w", err)
			}
			configDir := filepath.Join(home, ".config", "opencode")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", configDir, err)
			}
			configPath := filepath.Join(configDir, "opencode.jsonc")

			// Load existing config or start fresh
			cfg := make(map[string]any)
			data, err := os.ReadFile(configPath)
			if err == nil {
				if err := json.Unmarshal(data, &cfg); err != nil {
					// The .jsonc extension invites comments, but this rewrite goes
					// through encoding/json, which cannot read or preserve them.
					return fmt.Errorf("parse existing %s: %w\n"+
						"mycodeagent config rewrites this file as strict JSON — remove any // comments first "+
						"(they would be lost on the rewrite anyway)", configPath, err)
				}
			}

			cfg["$schema"] = "https://opencode.ai/config.json"

			// Ensure provider section
			providers, _ := cfg["provider"].(map[string]any)
			if providers == nil {
				providers = make(map[string]any)
			}

			// Remove providers from previous runs. This is the only part of the
			// file we own; everything else the user configured is left alone.
			removed := 0
			for k := range providers {
				if strings.HasPrefix(k, providerPrefix) {
					delete(providers, k)
					removed++
				}
			}

			var defaultModel string

			for _, inst := range instances {
				if inst.LocalPort == 0 {
					continue
				}

				// The model id opencode must send back. The engine launches the
				// server with `--alias model.Name`, and entity.Instance.ModelName is
				// that same value, so the stored name is already the right fallback.
				//
				// It must NOT be replaced with the catalog's HFRepo: that is the GGUF
				// repository path, which the server has never heard of. Writing it
				// made opencode send model=unsloth/Qwen3.6-27B-GGUF and get back
				// "model not found" — the probe below happened to paper over it
				// whenever the server was already up.
				modelID := inst.ModelName
				maxModelLen := 0
				alias := ""
				if m, err := app.FindModelByName(inst.ModelName); err == nil {
					maxModelLen = m.ContextLength
					alias = m.Alias
				}

				// Both values are better read from the server than assumed.
				//
				// The served context length is authoritative: scaledContextLength()
				// grows model.ContextLength linearly with per-GPU VRAM headroom, so a
				// rental with fatter GPUs than the catalog minimum will serve a larger
				// window than the static ContextLength claims. Writing the catalog value
				// into opencode's limit.context makes opencode refuse prompts that would
				// actually fit on the server, with the session bricking at compaction
				// time (pruned=0) and prompt_async failing synchronously with UnknownError.
				// llama.cpp can also auto-fit the window *down* to fit VRAM, in which
				// case the catalog value is too large and opencode oversends instead.
				if served, servedMaxLen, _ := app.GetServedModelInfo(ctx, inst.LocalPort); served != "" {
					modelID = served
					if servedMaxLen > 0 {
						maxModelLen = servedMaxLen
					}
				} else {
					// Still loading, or the tunnel is down. The entry is written from
					// catalog values, which may not match what the server ends up with.
					fmt.Printf("  warning: instance %d did not answer /v1/models — "+
						"context limit is the catalog value; re-run once it is healthy\n", inst.ID)
				}

				baseURL := fmt.Sprintf("http://localhost:%d/v1", inst.LocalPort)

				// Each instance gets its own provider (different ports).
				// Include the model alias when available so the provider key
				// is human-readable (e.g. mycodeagent-coder-23) instead of
				// just the instance ID.
				providerName := fmt.Sprintf("%s%d", providerPrefix, inst.ID)
				displayName := fmt.Sprintf("mycodeagent %s", inst.ModelName)
				if alias != "" {
					providerName = fmt.Sprintf("%s%s-%d", providerPrefix, alias, inst.ID)
					displayName = fmt.Sprintf("mycodeagent %s (%s)", alias, inst.ModelName)
				}
				providers[providerName] = map[string]any{
					"npm":  "@ai-sdk/openai-compatible",
					"name": displayName,
					"options": map[string]any{
						"baseURL": baseURL,
					},
					"models": map[string]any{
						modelID: buildModelConfig(modelID, maxModelLen),
					},
				}
				// First instance becomes the default
				if defaultModel == "" {
					defaultModel = providerName + "/" + modelID
				}

				fmt.Printf("  %s: %s → %s\n", providerName, modelID, baseURL)
			}

			// Drop the key entirely rather than leaving `"provider": {}` behind:
			// once our last instance is gone there is nothing left to declare, and
			// an empty object is noise in a file the user also edits by hand.
			if len(providers) == 0 {
				delete(cfg, "provider")
			} else {
				cfg["provider"] = providers
			}

			existingDefault, _ := cfg["model"].(string)
			if chosen, keep := chooseDefaultModel(existingDefault, defaultModel, providers); keep {
				cfg["model"] = chosen
			} else {
				// The default pointed at a mycodeagent provider we just removed and
				// there is no replacement. Leaving it would dangle, so drop the key
				// and let opencode fall back — loudly, since it is the user's setting.
				delete(cfg, "model")
				fmt.Printf("Cleared default model %q — its instance is gone. Set a new one in opencode.\n", existingDefault)
			}

			// `mode` is deliberately NOT written. It is deprecated in opencode
			// (superseded by `agent`), it is a *global* setting, and rewriting it
			// wholesale clobbered whatever the user had — including per-mode model
			// overrides. The sampling temperature these models want belongs on the
			// model entry instead, where it only affects our own providers.

			out, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, out, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", configPath, err)
			}

			fmt.Printf("Updated %s\n", configPath)
			if removed > 0 && len(instances) == 0 {
				fmt.Printf("Removed %d stale mycodeagent provider(s); nothing is running.\n", removed)
				fmt.Println("Deploy one with 'mycodeagent init <model>'.")
				return nil
			}
			if len(instances) == 0 {
				fmt.Println("No running instances — deploy one with 'mycodeagent init <model>'.")
				return nil
			}
			fmt.Printf("Default model: %s\n", cfg["model"])
			return nil
		},
	}

	return cmd
}

// chooseDefaultModel decides what opencode's global `model` key should become.
//
// existing is what the config already has ("" if unset), candidate is the first
// running mycodeagent model ("" if none), and providers is the freshly rebuilt
// provider map. A false second return means "delete the key".
//
// The rule that matters: `model` belongs to the user. We may claim it only when
// nothing is set, or when it already points at one of our own providers.
//
// The previous rule — keep the existing default if its provider key is present
// in the config's `provider` map — looked equivalent but was not. That map only
// ever holds providers declared *in the file*, while the providers a user
// actually subscribes to (opencode-go, openrouter, …) are known to opencode
// natively and never appear there. So `opencode-go/kimi-k2.6` failed the lookup
// and was silently overwritten with a local model on every single run.
func chooseDefaultModel(existing, candidate string, providers map[string]any) (string, bool) {
	if existing == "" {
		return candidate, candidate != ""
	}
	key, _, _ := strings.Cut(existing, "/")
	if !strings.HasPrefix(key, providerPrefix) {
		return existing, true // someone else's provider — hands off
	}
	if _, stillRunning := providers[key]; stillRunning {
		return existing, true // ours, and still up
	}
	// Ours but gone: replace it if we have something, otherwise clear it.
	return candidate, candidate != ""
}

// buildModelConfig renders one entry of a provider's `models` map.
//
// The temperature lives here rather than in a global `mode` block: every catalog
// model is a reasoning model, and greedy decoding degrades Qwen3 thinking, but
// that is a fact about *these* models — forcing it globally also re-tuned the
// user's subscription models, which is not ours to do.
func buildModelConfig(modelID string, maxModelLen int) map[string]any {
	m := map[string]any{
		"name":    modelID,
		"options": map[string]any{"temperature": 0.6},
	}
	if maxModelLen > 0 {
		m["limit"] = map[string]any{
			"context": maxModelLen,
			"output":  8192,
		}
	}
	return m
}
