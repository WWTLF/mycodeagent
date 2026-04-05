package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/infrastructure/config"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Configure vast.ai API key and HuggingFace token",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				cfg = &config.Config{BasePort: 8000}
			}

			reader := bufio.NewReader(os.Stdin)

			// Vast.ai credentials
			fmt.Println("===================================================================")
			fmt.Printf("Visit https://cloud.vast.ai/manage-keys/ to get your API token\n")
			fmt.Println("===================================================================")
			current := maskKey(cfg.VastaiAPIKey)
			fmt.Printf("Vast.ai API key   %s: ", current)
			apiKey, _ := reader.ReadString('\n')
			apiKey = strings.TrimSpace(apiKey)
			if apiKey != "" {
				cfg.VastaiAPIKey = apiKey
			}

			// HF token (optional for gated models like Llama 3, some Mistral variants)
			fmt.Println("===================================================================")
			fmt.Printf("Visit https://huggingface.co/settings/tokens to get a read token\n")
			fmt.Println("===================================================================")
			current = maskKey(cfg.HFToken)
			fmt.Printf("HuggingFace token %s: ", current)
			hfToken, _ := reader.ReadString('\n')
			hfToken = strings.TrimSpace(hfToken)
			if hfToken != "" {
				cfg.HFToken = hfToken
			}

			// Verify vast.ai API key before saving
			if cfg.VastaiAPIKey != "" {
				fmt.Print("Verifying vast.ai API key... ")
				client := vastai.NewClient(cfg.VastaiAPIKey)
				verbose, _ := cmd.Flags().GetBool("verbose")
				client.SetVerbose(verbose)
				if err := client.VerifyAPIKey(); err != nil {
					fmt.Println("FAILED")
					return fmt.Errorf("vast.ai API key verification failed: %w", err)
				}
				fmt.Println("OK")
			}

			// Check SSH key
			if cfg.VastaiAPIKey != "" {
				client := vastai.NewClient(cfg.VastaiAPIKey)
				verbose, _ := cmd.Flags().GetBool("verbose")
				client.SetVerbose(verbose)

				keys, err := client.ListSSHKeys()
				if err == nil && len(keys) == 0 {
					fmt.Println("===================================================================")
					fmt.Println("No SSH key found on your vast.ai account.")
					pubKey := findSSHPubKey()
					if pubKey != "" {
						fmt.Printf("Found local key: %s...%s\n", pubKey[:20], pubKey[len(pubKey)-20:])
						fmt.Print("Upload to vast.ai? [Y/n]: ")
						answer, _ := reader.ReadString('\n')
						answer = strings.TrimSpace(strings.ToLower(answer))
						if answer == "" || answer == "y" || answer == "yes" {
							if err := client.CreateSSHKey(pubKey); err != nil {
								fmt.Printf("Failed to upload SSH key: %v\n", err)
							} else {
								fmt.Println("SSH key uploaded.")
							}
						}
					} else {
						fmt.Println("No SSH key found at ~/.ssh/id_*.pub")
						fmt.Println("Generate one with: ssh-keygen -t ed25519")
						fmt.Println("Then run 'mycodeagent login' again.")
					}
				} else if err == nil {
					fmt.Printf("SSH key: already configured (%d key(s) on vast.ai)\n", len(keys))
				}
			}

			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			configPath, _ := config.ConfigPath()
			fmt.Printf("Saved to %s\n", configPath)
			fmt.Println("===================================================================")
			return nil
		},
	}
}

func findSSHPubKey() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	sshDir := filepath.Join(homeDir, ".ssh")
	// Try common key names in order of preference
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
		data, err := os.ReadFile(filepath.Join(sshDir, name))
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

func maskKey(key string) string {
	if key == "" {
		return "[not set]"
	}
	if len(key) <= 8 {
		return "[***]"
	}
	return fmt.Sprintf("[***%s]", key[len(key)-4:])
}
