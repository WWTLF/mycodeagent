package commands

import (
	"context"
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewStopCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <id>",
		Short: "Stop an instance (keeps disk, can restart later)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}
			if err := app.Stop(context.Background(), id); err != nil {
				return err
			}
			fmt.Printf("Instance %d stopped.\n", id)
			return nil
		},
	}
}
