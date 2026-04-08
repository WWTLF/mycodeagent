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

			fmt.Printf("Tunnel established: http://localhost:%d/v1 (PID %d)\n", inst.LocalPort, inst.TunnelPID)

			// Brief pause for tunnel to stabilize before health probe.
			time.Sleep(3 * time.Second)
			fmt.Print("Verifying vLLM... ")
			id, maxLen, err := app.GetServedModelInfo(ctx, inst.LocalPort)
			if err != nil || id == "" {
				fmt.Println("not responding (vLLM may still be loading)")
				return nil
			}
			fmt.Printf("OK — %s (context: %d)\n", id, maxLen)
			return nil
		},
	}
}
