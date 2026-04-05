package commands

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func NewBudgetCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "budget",
		Short: "Show consumption by instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			instances, err := app.Instances.FindAll()
			if err != nil {
				return err
			}
			if len(instances) == 0 {
				fmt.Println("No instances found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tMODEL\tSTATUS\t$/HR\tHOURS\tTOTAL $")
			fmt.Fprintln(w, "--\t-----\t------\t----\t-----\t-------")

			var grandTotal float64
			for _, inst := range instances {
				hours := time.Since(inst.CreatedAt).Hours()
				if inst.Status == "stopped" {
					// For stopped instances, use a rough estimate
					hours = 0
				}
				cost := inst.HourlyRate * hours
				grandTotal += cost
				fmt.Fprintf(w, "%d\t%s\t%s\t%.3f\t%.1f\t%.2f\n",
					inst.ID, inst.ModelName, inst.Status, inst.HourlyRate, hours, cost)
			}
			w.Flush()
			fmt.Printf("\nTotal estimated spend: $%.2f\n", grandTotal)
			return nil
		},
	}
}
