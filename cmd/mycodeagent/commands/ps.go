package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewPsCmd(app *application.App, vastaiClient *vastai.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List deployed instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Fetch live instances from vast.ai
			remoteInstances, err := vastaiClient.ListInstances()
			if err != nil {
				return fmt.Errorf("fetch instances from vast.ai: %w", err)
			}

			if len(remoteInstances) == 0 {
				fmt.Println("No instances on vast.ai.")
				return nil
			}

			// Build local DB lookup by vastai_id
			localInstances, _ := app.Instances.FindAll()
			localMap := make(map[int64]localInfo)
			for _, li := range localInstances {
				localMap[li.VastaiID] = localInfo{
					modelName: li.ModelName,
					localPort: li.LocalPort,
					tunnelPID: li.TunnelPID,
				}
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "VAST ID\tSTATUS\tMODEL\tTUNNEL URL")
			fmt.Fprintln(w, "-------\t------\t-----\t----------")

			for _, ri := range remoteInstances {
				vastID := int64(ri.ID)
				model := "unknown"
				tunnelURL := "-"

				if li, ok := localMap[vastID]; ok {
					model = li.modelName
					if li.localPort > 0 {
						tunnelURL = fmt.Sprintf("http://localhost:%d/v1", li.localPort)
					}
				}

				// If model unknown and instance is running, try to detect via vLLM API
				if model == "unknown" && ri.ActualStatus == "running" {
					if li, ok := localMap[vastID]; ok && li.localPort > 0 {
						if detected := detectModel(li.localPort); detected != "" {
							model = detected
						}
					}
				}

				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					ri.ID, ri.ActualStatus, model, tunnelURL)
			}
			return w.Flush()
		},
	}
}

type localInfo struct {
	modelName string
	localPort int
	tunnelPID int
}

// detectModel calls the vLLM /v1/models endpoint via the local tunnel to find out which model is served.
func detectModel(localPort int) string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/v1/models", localPort))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	if len(result.Data) > 0 {
		return result.Data[0].ID
	}
	return ""
}
