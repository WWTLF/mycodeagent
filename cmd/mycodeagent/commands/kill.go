package commands

import (
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/spf13/cobra"
)

func NewKillCmd(deploySvc *service.DeployService) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <id>",
		Short: "Destroy an instance permanently",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}
			if err := deploySvc.Destroy(id); err != nil {
				return err
			}
			fmt.Printf("Instance %d destroyed.\n", id)
			return nil
		},
	}
}
