package commands

import (
	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/spf13/cobra"
)

func NewInitCmd(deploySvc *service.DeployService) *cobra.Command {
	return &cobra.Command{
		Use:   "init <model>",
		Short: "Deploy a model on vast.ai",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := deploySvc.Deploy(args[0])
			return err
		},
	}
}
