package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewPsCmd(app *application.App, vastaiClient *vastai.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List deployed instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			remoteInstances, err := vastaiClient.ListInstances()
			if err != nil {
				return fmt.Errorf("fetch instances from vast.ai: %w", err)
			}

			// Build remote lookup
			remoteMap := make(map[int64]vastai.InstanceInfo)
			for _, ri := range remoteInstances {
				remoteMap[int64(ri.ID)] = ri
			}

			// Reconcile: for every remote instance, UPSERT into SQLite keyed on vastai_id.
			// Never delete rows just because ListInstances succeeds — a transient API hiccup
			// shouldn't wipe local tunnel/port state. Rows for instances that truly vanished
			// from vast.ai are cleaned up by `kill` / explicit commands.
			localInstances, err := app.Instances.FindAll()
			if err != nil {
				return fmt.Errorf("read local instances: %w", err)
			}
			localByVastID := make(map[int64]*entity.Instance, len(localInstances))
			dupes := make(map[int64][]int64) // vastai_id → extra local ids to drop
			for _, li := range localInstances {
				if existing, ok := localByVastID[li.VastaiID]; ok {
					// Historical duplicate row for the same vast.ai instance — schedule for deletion.
					dupes[li.VastaiID] = append(dupes[li.VastaiID], li.ID)
					// Keep whichever row has more useful state (tunnel info wins).
					if li.LocalPort > 0 && existing.LocalPort == 0 {
						dupes[li.VastaiID] = append(dupes[li.VastaiID], existing.ID)
						localByVastID[li.VastaiID] = li
					}
					continue
				}
				localByVastID[li.VastaiID] = li
			}
			// Drop historical duplicates so we stop printing the same instance N times.
			for _, ids := range dupes {
				for _, id := range ids {
					app.Instances.Delete(id)
				}
			}

			for _, ri := range remoteInstances {
				vastID := int64(ri.ID)
				status := ri.ActualStatus
				if ri.CurState == "stopped" || ri.CurState == "exited" {
					status = ri.CurState
				}
				if ri.StatusMsg != "" {
					status = fmt.Sprintf("%s (%s)", status, ri.StatusMsg)
				}

				if local, ok := localByVastID[vastID]; ok {
					// UPDATE existing row — preserve tunnel fields we own locally.
					local.Status = entity.InstanceStatus(status)
					if ri.SSHHost != "" {
						local.SSHHost = ri.SSHHost
					}
					if p := ri.GetSSHPort(); p > 0 {
						local.SSHPort = p
					}
					local.HourlyRate = ri.DPHTotal
					if err := app.Instances.Update(local); err != nil {
						fmt.Fprintf(os.Stderr, "update instance %d: %v\n", local.ID, err)
					}
					continue
				}

				// INSERT new row.
				inst := &entity.Instance{
					VastaiID:   vastID,
					ModelName:  detectModelFromOnstart(ri.Onstart, app),
					Status:     entity.InstanceStatus(status),
					SSHHost:    ri.SSHHost,
					SSHPort:    ri.GetSSHPort(),
					HourlyRate: ri.DPHTotal,
				}
				if err := app.Instances.Save(inst); err != nil {
					fmt.Fprintf(os.Stderr, "save instance %d: %v\n", vastID, err)
				}
			}

			// 3. Display
			localInstances, _ = app.Instances.FindAll()
			if len(localInstances) == 0 {
				fmt.Println("No instances.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tVAST ID\tSTATUS\tMODEL\tVOLUME\tHEALTH\tTUNNEL URL")
			fmt.Fprintln(w, "--\t-------\t------\t-----\t------\t------\t----------")

			for _, inst := range localInstances {
				tunnelURL := "-"
				health := "-"
				if inst.LocalPort > 0 {
					tunnelURL = fmt.Sprintf("http://localhost:%d/v1", inst.LocalPort)
					health = checkHealth(inst.LocalPort)
				}

				// Try to detect model via vLLM API if unknown and tunnel is up
				if inst.ModelName == "unknown" && inst.LocalPort > 0 {
					if detected := detectModel(inst.LocalPort); detected != "" {
						inst.ModelName = detected
						app.Instances.Update(inst)
					}
				}

				// Extract volume from remote instance's extra_env (e.g. ["-v V.123:/path", "1"])
				volName := "-"
				if ri, ok := remoteMap[inst.VastaiID]; ok {
					volName = extractVolumeName(ri)
				}

				fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
					inst.ID, inst.VastaiID, inst.Status, inst.ModelName, volName, health, tunnelURL)
			}
			return w.Flush()
		},
	}
}

func checkHealth(localPort int) string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", localPort))
	if err != nil {
		return "unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		return "healthy"
	}
	return fmt.Sprintf("unhealthy (%d)", resp.StatusCode)
}

func extractVolumeName(ri vastai.InstanceInfo) string {
	for _, env := range ri.ExtraEnv {
		if len(env) > 0 && strings.HasPrefix(env[0], "-v ") {
			// "-v V.123456:/mount/path" → "V.123456"
			vol := strings.TrimPrefix(env[0], "-v ")
			if idx := strings.Index(vol, ":"); idx > 0 {
				return vol[:idx]
			}
			return vol
		}
	}
	return "-"
}

func detectModelFromOnstart(onstart string, app *application.App) string {
	models, _ := app.Models.FindAll()
	for _, m := range models {
		if strings.Contains(onstart, m.HFRepo) {
			return m.Name
		}
	}
	if idx := strings.Index(onstart, "vllm serve '"); idx >= 0 {
		rest := onstart[idx+len("vllm serve '"):]
		if end := strings.Index(rest, "'"); end >= 0 {
			return rest[:end]
		}
	}
	return "unknown"
}

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
