package commands

import (
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewHostsCmd(app *application.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "Manage known-bad vast.ai hosts (auto-skipped during deploy)",
	}
	cmd.AddCommand(
		newHostsListCmd(app),
		newHostsRemoveCmd(app),
		newHostsClearCmd(app),
	)
	return cmd
}

func newHostsListCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List machine IDs that failed to provision and are being skipped",
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := app.ListBadHosts()
			if err != nil {
				return err
			}
			if len(hosts) == 0 {
				fmt.Println("No bad hosts recorded.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MACHINE ID\tRECORDED AT\tREASON")
			fmt.Fprintln(w, "----------\t-----------\t------")
			for _, h := range hosts {
				fmt.Fprintf(w, "%d\t%s\t%s\n", h.MachineID, h.CreatedAt.Format("2006-01-02 15:04:05"), h.Reason)
			}
			return w.Flush()
		},
	}
}

func newHostsRemoveCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <machine_id>",
		Short: "Un-blacklist a specific machine so it can be selected again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid machine_id: %w", err)
			}
			if err := app.RemoveBadHost(id); err != nil {
				return err
			}
			fmt.Printf("Machine %d removed from bad-host list.\n", id)
			return nil
		},
	}
}

func newHostsClearCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove all bad-host entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ClearBadHosts(); err != nil {
				return err
			}
			fmt.Println("Bad-host list cleared.")
			return nil
		},
	}
}
