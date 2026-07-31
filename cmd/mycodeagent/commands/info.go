package commands

import (
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/spf13/cobra"
)

func NewInfoCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "info [id]",
		Short: "Show how to configure opencode to use deployed models",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 1 {
				id, err := strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("invalid instance ID: %s", args[0])
				}
				instances, err := app.ListInstances(ctx)
				if err != nil {
					return err
				}
				var inst *entity.Instance
				for _, i := range instances {
					if i.ID == id {
						inst = i
						break
					}
				}
				if inst == nil {
					return fmt.Errorf("instance %d not found", id)
				}
				baseURL := app.TunnelURL(inst)
				_, openAI := app.HealthProbe(inst)
				hfRepo := inst.ModelName
				if m, err := app.FindModelByName(inst.ModelName); err == nil {
					hfRepo = m.HFRepo
				}
				printInstanceInfo(inst.ID, inst.ModelName, string(inst.Status), baseURL, hfRepo, openAI)
				return nil
			}

			instances, _ := app.ListInstances(ctx)

			if len(instances) == 0 {
				fmt.Println("No running instances.")
				fmt.Println()
				fmt.Println("Run 'mycodeagent init <model>' to deploy, then 'mycodeagent info <id>' for config.")
				return nil
			}

			var running []*entity.Instance
			for _, inst := range instances {
				if inst.Status == entity.StatusRunning {
					running = append(running, inst)
				}
			}

			if len(running) == 0 {
				fmt.Println("No running instances.")
				return nil
			}

			if len(running) == 1 {
				inst := running[0]
				baseURL := app.TunnelURL(inst)
				_, openAI := app.HealthProbe(inst)
				hfRepo := inst.ModelName
				if m, err := app.FindModelByName(inst.ModelName); err == nil {
					hfRepo = m.HFRepo
				}
				printInstanceInfo(inst.ID, inst.ModelName, string(inst.Status), baseURL, hfRepo, openAI)
				return nil
			}

			fmt.Println("Multiple running instances. Use 'mycodeagent info <id>' for specific config:")
			fmt.Println()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  ID\tMODEL\tURL")
			fmt.Fprintln(w, "  --\t-----\t---")
			for _, inst := range running {
				fmt.Fprintf(w, "  %d\t%s\t%s\n", inst.ID, inst.ModelName, app.TunnelURL(inst))
			}
			return w.Flush()
		},
	}
}

// printInstanceInfo shows how to reach an instance.
//
// openAI gates everything below the URL: an opencode provider block and
// OPENAI_BASE_URL are meaningless for ComfyUI and Jupyter, which serve a web UI
// and no API. Printing them anyway was instructions that could not work.
func printInstanceInfo(id int64, modelName, status, baseURL, hfRepo string, openAI bool) {
	fmt.Printf("=== Instance %d: %s (%s) ===\n", id, modelName, status)
	fmt.Println()
	if !openAI {
		fmt.Println("Open in a browser:", baseURL)
		fmt.Println()
		return
	}
	fmt.Println("Endpoint:", baseURL)
	fmt.Println()
	fmt.Println("--- opencode.json ---")
	fmt.Println()
	printExampleConfig(baseURL, hfRepo)
	fmt.Println()
	fmt.Println("--- environment variables ---")
	fmt.Println()
	fmt.Printf("  export OPENAI_BASE_URL=%s\n", baseURL)
	fmt.Println("  export OPENAI_API_KEY=not-needed")
	fmt.Println()
}

func printExampleConfig(baseURL, hfRepo string) {
	fmt.Printf(`{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "mycodeagent": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "mycodeagent llama.cpp",
      "options": {
        "baseURL": "%s"
      },
      "models": {
        "%s": {
          "name": "%s"
        }
      }
    }
  },
  "model": "mycodeagent/%s"
}
`, baseURL, hfRepo, hfRepo, hfRepo)
}
