package commands

import (
	"fmt"
	"strings"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/spf13/cobra"
)

// syncFolderHelp explains where an engine's files land locally.
const syncFolderHelp = `--sync-folder chooses the local directory the instance's files are kept in.

Instances are disposable, so anything an engine writes — notebooks for Jupyter,
generated images and workflows for ComfyUI — is copied here every 60s and would
otherwise die with the machine. The default is ./` + entity.DefaultSyncRootName + ` in the
directory init runs from.

  --sync-folder ~/notebooks
  --sync-folder .            the current directory itself

The path is used as given. An engine with one synced directory — Jupyter, whose
/workspace holds the notebooks — syncs into it directly, so pointing the flag at
a project makes that project the workspace. ComfyUI has two (output/ and
workflows/) and keeps them as subfolders, because merging them would upload every
generated image into the instance's workflow folder.

The path is resolved to an absolute one and stored on the instance, so a later
'tunnel' or 'start' run from anywhere else still syncs to the same place.

Engines that write nothing (llama.cpp) ignore it.`

// countryHelp lists the codes worth knowing. It is not the ISO table — it is the
// set that actually had rentable offers when this was written, because a code
// with no machines behind it is just a way to get "no offers found".
// provisioningHelp explains the one mechanism that makes disposable instances
// workable for image generation: fetch the models on every boot instead of
// carrying them.
const provisioningHelp = `--provisioning takes the URL of a shell script the instance downloads and runs
before its service starts. ComfyUI supports this; llama.cpp ignores it.

It is how checkpoints and LoRAs get onto a machine that is destroyed after use:
put the downloads in the script rather than copying weights back and forth. Set
tokens once with 'mycodeagent login' and the script can reach gated models —
HF_TOKEN for HuggingFace (FLUX, SD3), CIVITAI_TOKEN for civitai.com.

  --provisioning https://raw.githubusercontent.com/you/yours/main/comfy.sh

The script runs on your rented machine with those tokens in its environment, so
point it only at something you control.`

const countryHelp = `Restrict the offer search to these countries (ISO-3166 alpha-2, comma-separated).

Offers are otherwise picked purely by price, which is how three deploys in a row
landed in a region whose route to HuggingFace ran at ~1 MB/s.

  Americas   US CA
  Europe     NO DK FR DE NL BE ES PL CZ HU RO UA TR
  Asia       KR JP HK TW CN VN TH
  Oceania    NZ

Codes are matched case-insensitively. Examples:

  --country FR,DE,NO      rent in western Europe
  --country US            United States only

Availability moves constantly; run 'mycodeagent models' to see live pricing, and
expect a narrow filter to cost more or find nothing at all.`

// parseCountries turns the raw flag into validated upper-case codes.
//
// Validating here rather than letting the API decide matters: vast.ai answers an
// unknown code with an empty result set, which is indistinguishable from "your
// filters are too tight" and sends you hunting for the wrong problem.
func parseCountries(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code == "" {
			continue
		}
		if len(code) != 2 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return nil, fmt.Errorf("invalid country code %q: expected two letters, e.g. US or DE", part)
		}
		if !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out, nil
}

func NewInitCmd(app *application.App) *cobra.Command {
	var createOnly bool
	var country string
	var provisioning string
	var syncFolder string

	cmd := &cobra.Command{
		Use:   "init <model>",
		Short: "Deploy a model on vast.ai",
		Long: "Rent a GPU, start llama-server and open an SSH tunnel.\n\n" +
			"The instance is destroyed automatically if startup fails, so a broken\n" +
			"deploy never leaves a paid GPU running.\n\n" +
			provisioningHelp + "\n\n" + syncFolderHelp + "\n\n" + countryHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// cmd.Context() carries the SIGINT cancellation wired in main.go —
			// Ctrl-C during a deploy must tear the paid instance down, not just
			// kill this process.
			ctx := cmd.Context()

			countries, err := parseCountries(country)
			if err != nil {
				return err
			}

			opts := service.DeployOptions{
				Countries:          countries,
				ProvisioningScript: provisioning,
				SyncFolder:         syncFolder,
			}

			if createOnly {
				result, err := app.DeployCreateOnly(ctx, args[0], opts)
				if err != nil {
					return err
				}
				inst := result.Instance

				fmt.Println()
				fmt.Println("Instance is running (no tunnel/health check).")
				fmt.Println()
				fmt.Printf("  Instance ID : %d\n", inst.VastaiID)
				fmt.Printf("  Model       : %s\n", inst.ModelName)
				fmt.Printf("  SSH Host    : %s\n", inst.SSHHost)
				fmt.Printf("  SSH Port    : %d\n", inst.SSHPort)
				fmt.Printf("  Rate        : $%.3f/hr\n", inst.HourlyRate)
				fmt.Println()
				fmt.Println("Connect via SSH:")
				fmt.Printf("  ssh -p %d root@%s\n", inst.SSHPort, inst.SSHHost)
				fmt.Println()
				fmt.Println("Run model server on the instance:")
				fmt.Printf("  %s\n", result.ServeCommand)
				fmt.Println()
				fmt.Println("Set up SSH tunnel to llama-server (port 8000):")
				fmt.Printf("  ssh -p %d root@%s -L 8080:localhost:8000\n", inst.SSHPort, inst.SSHHost)
				fmt.Println()
				fmt.Println("Once the tunnel is up, the API will be at: http://localhost:8080/v1")
				return nil
			}

			_, err = app.Deploy(ctx, args[0], opts)
			return err
		},
	}

	cmd.Flags().BoolVar(&createOnly, "create-instance-only", false, "Create instance and show SSH details without setting up tunnel or waiting for the model server")
	cmd.Flags().StringVar(&country, "country", "", "Comma-separated ISO-3166 alpha-2 country codes to rent in (see --help for the list)")
	cmd.Flags().StringVar(&provisioning, "provisioning", "", "URL of a script the instance runs before starting, to fetch models (ComfyUI only)")
	cmd.Flags().StringVar(&syncFolder, "sync-folder", "", "Local directory to sync notebooks / ComfyUI output into (default ./"+entity.DefaultSyncRootName+")")

	return cmd
}
