package commands

import (
	"fmt"

	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/spf13/cobra"
)

func NewKillCmd(deploySvc *service.DeployService) *cobra.Command {
	return &cobra.Command{
		Use:   "kill",
		Short: "Stop all running instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := deploySvc.KillAll(); err != nil {
				return err
			}
			fmt.Println("All instances stopped.")
			return nil
		},
	}
}
