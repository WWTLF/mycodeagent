package commands

import (
	"fmt"
	"strconv"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewTunnelCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "tunnel <id>",
		Short: "Re-establish SSH tunnel to an existing instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vastaiID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}

			fmt.Println("Re-establishing tunnel...")
			inst, err := app.EstablishTunnel(ctx, vastaiID)
			if err != nil {
				return err
			}

			fmt.Printf("Tunnel established: %s (PID %d)\n", app.TunnelURL(inst), inst.TunnelPID)

			// Brief pause for tunnel to stabilize before health probe.
			time.Sleep(3 * time.Second)

			// GetServedModelInfo reads /v1/models, which only the OpenAI engines
			// have. Asking ComfyUI or Jupyter for it reported "not responding" on
			// an instance that was serving perfectly well.
			probeURL, expectModelList := app.HealthProbe(inst)
			if !expectModelList {
				fmt.Print("Verifying server... ")
				fmt.Println(checkHealth(probeURL, false))
				return nil
			}

			fmt.Print("Verifying model server... ")
			id, maxLen, err := app.GetServedModelInfo(ctx, inst.LocalPort)
			if err != nil || id == "" {
				fmt.Println("not responding (the model may still be loading)")
				return nil
			}
			fmt.Printf("OK — %s (context: %d)\n", id, maxLen)
			return nil
		},
	}
}
