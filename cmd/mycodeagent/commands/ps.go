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
					tunnelURL = fmt.Sprintf("http://localhost:%d/v1", inst.LocalPort)
					health = checkHealth(inst.LocalPort)
				}

				// Try to detect model via vLLM API if unknown and tunnel is up.
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

func checkHealth(localPort int) string {
	// Use /v1/models as a universal OpenAI-compatible probe. vLLM's /health
	// endpoint works but LM Studio doesn't implement it (it 200s everything
	// unknown with an error body), so /health alone gives false positives.
	// /v1/models is implemented correctly by both and returns a "data" array
	// containing the loaded model(s). Valid shape ⇒ actually serving.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/v1/models", localPort))
	if err != nil {
		return "unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Sprintf("unhealthy (%d)", resp.StatusCode)
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
