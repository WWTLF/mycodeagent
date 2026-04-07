package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewConfigCmd(app *application.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Update the userwide opencode config with all running instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			instances, err := app.Instances.FindRunning()
			if err != nil {
				return err
			}
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
				if m, err := app.Models.FindByName(inst.ModelName); err == nil {
					hfRepo = m.HFRepo
					maxModelLen = m.ContextLength
				}

				// Authoritative model ID: ask the server directly. vLLM reports
				// the HF repo (matching the static entry), but LM Studio reports
				// the lowercased GGUF filename — if we use the static HFRepo for
				// LM Studio instances, opencode sends model=... and gets "model
				// not found". Always prefer what the server actually answers to.
				if served := detectModel(inst.LocalPort); served != "" {
					hfRepo = served
				}

				baseURL := fmt.Sprintf("http://localhost:%d/v1", inst.LocalPort)

				// Each instance gets its own provider (different ports)
				providerName := fmt.Sprintf("mycodeagent-%d", inst.ID)
				providers[providerName] = map[string]any{
					"npm":  "@ai-sdk/openai-compatible",
					"name": fmt.Sprintf("mycodeagent %s", inst.ModelName),
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
