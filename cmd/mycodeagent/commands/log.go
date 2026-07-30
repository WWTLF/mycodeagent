package commands

import (
	"fmt"
	"os"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewLogCmd(app *application.App) *cobra.Command {
	var tail string

	cmd := &cobra.Command{
		Use:   "log <id>",
		Short: "Fetch the vast.ai bootstrap log from a running instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}

			inst, err := app.FindInstanceByID(ctx, id)
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "Requesting logs from vast.ai...\n")
			data, err := app.GetLogs(ctx, inst.ID, tail)
			if err != nil {
				return err
			}
			os.Stdout.Write(data)
			return nil
		},
	}

	cmd.Flags().StringVarP(&tail, "tail", "n", "100", "Number of lines to show from end of logs")
	return cmd
}
