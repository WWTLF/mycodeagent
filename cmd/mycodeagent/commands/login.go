package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewLoginCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Configure vast.ai API key and HuggingFace token",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			reader := bufio.NewReader(os.Stdin)

			// Vast.ai credentials
			fmt.Println("===================================================================")
			fmt.Printf("Visit https://cloud.vast.ai/manage-keys/ to get your API token\n")
			fmt.Println("===================================================================")
			fmt.Printf("Vast.ai API key   %s: ", maskKey(app.VastaiAPIKey()))
			apiKeyInput, _ := reader.ReadString('\n')
			apiKeyInput = strings.TrimSpace(apiKeyInput)

			// HF token (optional for gated models like Llama 3, some Mistral variants)
			fmt.Println("===================================================================")
			fmt.Printf("Visit https://huggingface.co/settings/tokens to get a read token\n")
			fmt.Println("===================================================================")
			fmt.Printf("HuggingFace token %s: ", maskKey(app.HFToken()))
			hfTokenInput, _ := reader.ReadString('\n')
			hfTokenInput = strings.TrimSpace(hfTokenInput)

			// Pick the effective vast.ai key for verification: new one if
			// supplied, otherwise the existing one. We need a non-empty key
			// to do anything meaningful (verify, list SSH keys, etc.).
			effectiveKey := apiKeyInput
			if effectiveKey == "" {
				effectiveKey = app.VastaiAPIKey()
			}

			// Local SSH pub key — only relevant when we have a key to act on.
			pubKey := ""
			autoUpload := ""
			if effectiveKey != "" {
				pubKey = findSSHPubKey()
				if pubKey == "" {
					fmt.Println("No SSH key found at ~/.ssh/id_*.pub")
					fmt.Println("Generate one with: ssh-keygen -t ed25519")
					fmt.Println("Then run 'mycodeagent login' again to upload it.")
				} else {
					// We don't know yet whether the key is already on the
					// remote — Login will check. Pre-decide whether to upload
					// if it isn't there. Default: yes (matches old prompt).
					fmt.Printf("Found local SSH key: %s...%s\n", pubKey[:20], pubKey[len(pubKey)-20:])
					fmt.Print("Upload to vast.ai if not already registered? [Y/n]: ")
					answer, _ := reader.ReadString('\n')
					answer = strings.TrimSpace(strings.ToLower(answer))
					if answer == "" || answer == "y" || answer == "yes" {
						autoUpload = pubKey
					}
				}
			}

			fmt.Println()
			fmt.Print("Saving... ")
			result, err := app.Login(ctx, application.LoginInput{
				VastaiKey:       apiKeyInput,
				HFToken:         hfTokenInput,
				UploadSSHPubKey: autoUpload,
			})
			if err != nil {
				fmt.Println("FAILED")
				return err
			}
			fmt.Println("OK")

			if result.KeyVerified {
				fmt.Println("Vast.ai API key: verified")
			}
			if autoUpload != "" {
				if result.SSHKeyUploaded {
					fmt.Println("SSH key: uploaded to vast.ai")
				} else if pubKey != "" {
					fmt.Printf("SSH key: already on vast.ai (%d key(s) on account)\n", result.SSHKeysOnRemote)
				}
			} else if result.SSHKeysOnRemote > 0 {
				fmt.Printf("SSH key: %d key(s) on vast.ai account\n", result.SSHKeysOnRemote)
			}

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
