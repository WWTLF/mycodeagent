package commands

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/config"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewModelsCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List available models",
		RunE: func(cmd *cobra.Command, args []string) error {
			models, err := app.Models.FindAll()
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			// Fetch current cheapest prices if API key is configured
			cfg, _ := config.Load()
			prices := make(map[string]float64)
			if cfg != nil && cfg.VastaiAPIKey != "" {
				client := vastai.NewClient(cfg.VastaiAPIKey)
				for _, m := range models {
					numGPUs := m.NumGPUs
					if numGPUs <= 0 {
						numGPUs = 1
					}
					offers, err := client.SearchOffers(m.VRAM, numGPUs)
					if err == nil && len(offers) > 0 {
						prices[m.Name] = offers[0].DPHTotal
					}
				}
			}

			fmt.Fprintln(w, "ALIAS\tNAME\tGPUs\tENGINE\t$/HR\tHF REPO")
			fmt.Fprintln(w, "-----\t----\t----\t------\t----\t-------")
			for _, m := range models {
				numGPUs := m.NumGPUs
				if numGPUs <= 0 {
					numGPUs = 1
				}
				gpuStr := fmt.Sprintf("%dx %dGB", numGPUs, m.VRAM)
				engineStr := string(m.Engine)
				priceStr := "-"
				if p, ok := prices[m.Name]; ok {
					priceStr = fmt.Sprintf("$%.3f", p)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", m.Alias, m.Name, gpuStr, engineStr, priceStr, m.HFRepo)
			}
			return w.Flush()
		},
	}
}
