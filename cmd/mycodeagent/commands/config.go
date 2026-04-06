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
	var dir string

	cmd := &cobra.Command{
		Use:   "config",
		Short: "Update opencode.jsonc with all running instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			instances, err := app.Instances.FindRunning()
			if err != nil {
				return err
			}
			if len(instances) == 0 {
				return fmt.Errorf("no running instances with tunnels — deploy first with 'mycodeagent init'")
			}

			configPath := filepath.Join(dir, "opencode.jsonc")

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

				hfRepo := inst.ModelName
				maxModelLen := 0
				if m, err := app.Models.FindByName(inst.ModelName); err == nil {
					hfRepo = m.HFRepo
					for i, arg := range m.VLLMArgs {
						if arg == "--max-model-len" && i+1 < len(m.VLLMArgs) {
							fmt.Sscanf(m.VLLMArgs[i+1], "%d", &maxModelLen)
						}
					}
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

			// Add Go LSP (gopls)
			lsp, _ := cfg["lsp"].(map[string]any)
			if lsp == nil {
				lsp = make(map[string]any)
			}
			lsp["golang"] = map[string]any{
				"command": "gopls",
				"args":    []string{"serve"},
			}
			cfg["lsp"] = lsp

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

	cmd.Flags().StringVarP(&dir, "dir", "d", ".", "Directory to write opencode.jsonc")
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
