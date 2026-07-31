package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewPsCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List deployed instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// SyncInstances pulls from vast.ai, reconciles with the local DB,
			// and returns the reconciled list. All the dedupe / insert / delete
			// / update logic that used to be inlined here lives in
			// InstanceService.Sync now.
			instances, err := app.SyncInstances(ctx)
			if err != nil {
				return err
			}
			if len(instances) == 0 {
				fmt.Println("No instances.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tVAST ID\tSTATUS\tALIAS\tMODEL\tHEALTH\tTUNNEL URL")
			fmt.Fprintln(w, "--\t-------\t------\t-----\t-----\t------\t----------")

			for _, inst := range instances {
				tunnelURL := "-"
				health := "-"
				if inst.LocalPort > 0 {
					// Both come from the engine behind the instance's model. A
					// hardcoded /v1 here printed a 404 for ComfyUI and Jupyter and
					// reported every healthy one of them as unhealthy.
					tunnelURL = app.TunnelURL(inst)
					probeURL, expectModels := app.HealthProbe(inst)
					health = checkHealth(probeURL, expectModels)
				}

				// Try to detect model via the served API if unknown and tunnel is up.
				if inst.ModelName == "unknown" && inst.LocalPort > 0 {
					if detected, _, _ := app.GetServedModelInfo(ctx, inst.LocalPort); detected != "" {
						inst.ModelName = detected
						_ = app.UpdateInstance(ctx, inst)
					}
				}

				// Look up the alias from the model definition.
				alias := "-"
				if m, err := app.FindModelByName(inst.ModelName); err == nil && m.Alias != "" {
					alias = m.Alias
				}

				fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
					inst.ID, inst.VastaiID, inst.Status, alias, inst.ModelName, health, tunnelURL)
			}
			return w.Flush()
		},
	}
}

// checkHealth GETs the engine's health route through the tunnel.
//
// expectModelList additionally requires an OpenAI "data" array holding the
// loaded model, which is how "serving" is told apart from "bound the port":
// llama-server answers 503 on every route but /health until the weights are in,
// and some servers 200 an unknown path with an error body, so status alone gives
// false positives.
//
// It applies to the OpenAI engines only. ComfyUI's /history answers `{}` and
// Jupyter's / answers HTML; demanding a model list of them marked every healthy
// instance unhealthy.
func checkHealth(url string, expectModelList bool) string {
	if url == "" {
		return "-"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Sprintf("unhealthy (%d)", resp.StatusCode)
	}
	if !expectModelList {
		return "healthy"
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || len(body.Data) == 0 {
		return "unhealthy"
	}
	return "healthy"
}
