package commands

import (
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewRestartCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "restart <id>",
		Short: "Restart the model server on a running instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}
			return app.Restart(cmd.Context(), id)
		},
	}
}
