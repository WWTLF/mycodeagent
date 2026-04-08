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
				if strings.HasPrefix(string(inst.Status), "running") || strings.HasPrefix(string(inst.Status), "starting") {
					runningInstances = append(runningInstances, inst)
				}
			}
			instances = runningInstances
			if len(instances) == 0 {
				return fmt.Errorf("no running instances with tunnels — deploy first with 'mycodeagent init'")
			}

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
					return fmt.Errorf("parse existing %s: %w", configPath, err)
				}
			}

			cfg["$schema"] = "https://opencode.ai/config.json"

			// Ensure provider section
			providers, _ := cfg["provider"].(map[string]any)
			if providers == nil {
				providers = make(map[string]any)
			}

			// Remove old mycodeagent providers
			for k := range providers {
				if strings.HasPrefix(k, "mycodeagent") {
					delete(providers, k)
				}
			}

			var defaultModel string

			for _, inst := range instances {
				if inst.LocalPort == 0 {
					continue
				}

				// Default: HF repo from the static catalog.
				hfRepo := inst.ModelName
				maxModelLen := 0
				alias := ""
				if m, err := app.FindModelByName(inst.ModelName); err == nil {
					hfRepo = m.HFRepo
					maxModelLen = m.ContextLength
					alias = m.Alias
				}

				// Authoritative model ID AND context length: ask the server directly.
				// vLLM reports the HF repo (matching the static entry), but LM Studio
				// reports the lowercased GGUF filename — if we use the static HFRepo
				// for LM Studio instances, opencode sends model=... and gets "model
				// not found". Always prefer what the server actually answers to.
				//
				// The served context length is also authoritative: scaledContextLength()
				// grows model.ContextLength linearly with per-GPU VRAM headroom, so a
				// rental with fatter GPUs than the catalog minimum will serve a larger
				// window than the static ContextLength claims. Writing the catalog value
				// into opencode's limit.context makes opencode refuse prompts that would
				// actually fit on the server, with the session bricking at compaction
				// time (pruned=0) and prompt_async failing synchronously with UnknownError.
				if served, servedMaxLen, _ := app.GetServedModelInfo(ctx, inst.LocalPort); served != "" {
					hfRepo = served
					if servedMaxLen > 0 {
						maxModelLen = servedMaxLen
					}
				}

				baseURL := fmt.Sprintf("http://localhost:%d/v1", inst.LocalPort)

				// Each instance gets its own provider (different ports).
				// Include the model alias when available so the provider key
				// is human-readable (e.g. mycodeagent-coder-23) instead of
				// just the instance ID.
				providerName := fmt.Sprintf("mycodeagent-%d", inst.ID)
				displayName := fmt.Sprintf("mycodeagent %s", inst.ModelName)
				if alias != "" {
					providerName = fmt.Sprintf("mycodeagent-%s-%d", alias, inst.ID)
					displayName = fmt.Sprintf("mycodeagent %s (%s)", alias, inst.ModelName)
				}
				providers[providerName] = map[string]any{
					"npm":  "@ai-sdk/openai-compatible",
					"name": displayName,
					"options": map[string]any{
						"baseURL": baseURL,
					},
					"models": map[string]any{
						hfRepo: buildModelConfig(hfRepo, maxModelLen),
					},
				}
				// First instance becomes the default
				if defaultModel == "" {
					defaultModel = providerName + "/" + hfRepo
				}

				fmt.Printf("  %s: %s → %s\n", providerName, hfRepo, baseURL)
			}

			cfg["provider"] = providers
			cfg["model"] = defaultModel

			// Set mode temperatures (Qwen3 thinking requires 0.6, greedy decoding breaks thinking)
			cfg["mode"] = map[string]any{
				"build":   map[string]any{"temperature": 0.6},
				"plan":    map[string]any{"temperature": 0.6},
				"analyze": map[string]any{"temperature": 0.6},
			}

			out, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, out, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", configPath, err)
			}

			fmt.Printf("Updated %s\n", configPath)
			fmt.Printf("Default model: %s\n", defaultModel)
			return nil
		},
	}

	return cmd
}

func buildModelConfig(hfRepo string, maxModelLen int) map[string]any {
	m := map[string]any{
		"name": hfRepo,
	}
	if maxModelLen > 0 {
		m["limit"] = map[string]any{
			"context": maxModelLen,
			"output":  8192,
		}
	}
	return m
}
