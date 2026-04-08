package commands

import (
	"context"
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewKillCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <id>",
		Short: "Destroy an instance permanently",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}
			if err := app.Destroy(context.Background(), id); err != nil {
				return err
			}
			fmt.Printf("Instance %d destroyed.\n", id)
			return nil
		},
	}
}
