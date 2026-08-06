package commands

import (
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewStartCmd(app *application.App) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "start <id>",
		Short: "Resume a stopped instance and re-establish its tunnel",
		Long: "Resume an instance previously stopped with `mycodeagent stop`.\n\n" +
			"The GGUF is still in the container-disk cache, so this skips the\n" +
			"HuggingFace download. vast.ai reassigns the SSH host and port on\n" +
			"resume, so a new tunnel is opened and the local port may change —\n" +
			"pass --port to keep it.\n\n" + portHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}
			return app.Start(cmd.Context(), id, port)
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, portFlagUsage)

	return cmd
}
