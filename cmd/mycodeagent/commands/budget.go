package commands

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewBudgetCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "budget",
		Short: "Show consumption by instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			summary, err := app.GetBudget(cmd.Context())
			if err != nil {
				return err
			}
			if len(summary.Lines) == 0 {
				fmt.Println("No instances found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tMODEL\tSTATUS\t$/HR\tHOURS\tTOTAL $")
			fmt.Fprintln(w, "--\t-----\t------\t----\t-----\t-------")

			var grandTotal float64
			for _, line := range summary.Lines {
				grandTotal += line.Cost
				fmt.Fprintf(w, "%d\t%s\t%s\t%.3f\t%.1f\t%.2f\n",
					line.ID, line.ModelName, line.Status, line.HourlyRate, line.Hours, line.Cost)
			}
			w.Flush()
			fmt.Printf("\nTotal estimated spend: $%.2f\n", grandTotal)
			return nil
		},
	}
}
