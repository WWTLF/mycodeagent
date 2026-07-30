package commands

import (
	"context"
	"fmt"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewInitCmd(app *application.App) *cobra.Command {
	var createOnly bool

	cmd := &cobra.Command{
		Use:   "init <model>",
		Short: "Deploy a model on vast.ai (--create-instance-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if createOnly {
				result, err := app.DeployCreateOnly(context.Background(), args[0])
				if err != nil {
					return err
				}
				inst := result.Instance

				fmt.Println()
				fmt.Println("Instance is running (no tunnel/health check).")
				fmt.Println()
				fmt.Printf("  Instance ID : %d\n", inst.VastaiID)
				fmt.Printf("  Model       : %s\n", inst.ModelName)
				fmt.Printf("  SSH Host    : %s\n", inst.SSHHost)
				fmt.Printf("  SSH Port    : %d\n", inst.SSHPort)
				fmt.Printf("  Rate        : $%.3f/hr\n", inst.HourlyRate)
				fmt.Println()
				fmt.Println("Connect via SSH:")
				fmt.Printf("  ssh -p %d root@%s\n", inst.SSHPort, inst.SSHHost)
				fmt.Println()
				fmt.Println("Run model server on the instance:")
				fmt.Printf("  %s\n", result.ServeCommand)
				fmt.Println()
				fmt.Println("Set up SSH tunnel to llama-server (port 8000):")
				fmt.Printf("  ssh -p %d root@%s -L 8080:localhost:8000\n", inst.SSHPort, inst.SSHHost)
				fmt.Println()
				fmt.Println("Once the tunnel is up, the API will be at: http://localhost:8080/v1")
				return nil
			}

			_, err := app.Deploy(context.Background(), args[0])
			return err
		},
	}

	cmd.Flags().BoolVar(&createOnly, "create-instance-only", false, "Create instance and show SSH details without setting up tunnel or waiting for the model server")

	return cmd
}
