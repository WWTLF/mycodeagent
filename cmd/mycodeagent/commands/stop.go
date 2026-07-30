package commands

import (
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewStopCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <id>",
		Short: "Stop an instance, keeping its disk (resume with `start`)",
		Long: "Release the GPU but keep the instance and its container disk.\n\n" +
			"The disk keeps billing around the clock (~$0.15/GB/month), which for a\n" +
			"typical model outweighs the download it saves. Use this to hold on to a\n" +
			"specific host or to pause mid-debugging — not to save money. When you're\n" +
			"done for the day, `kill` is the cheaper end.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}
			if err := app.Stop(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Printf("Instance %d stopped. Disk still billing — resume with `mycodeagent start %d`, or free it with `mycodeagent kill %d`.\n", id, id, id)
			return nil
		},
	}
}
