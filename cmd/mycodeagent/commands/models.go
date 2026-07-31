package commands

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/spf13/cobra"
)

func boolMark(v bool) string {
	if v {
		return "+"
	}
	return "-"
}

func formatContext(ctx int) string {
	if ctx <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dK", ctx/1024)
}

func NewModelsCmd(app *application.App) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List available models",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			models, err := app.ListModels()
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

			// Fetch current cheapest prices via the offer search.
			prices := make(map[string]float64)
			for _, m := range models {
				offers, err := app.SearchOffers(ctx, m)
				if err == nil && len(offers) > 0 {
					prices[m.Name] = offers[0].DPHTotal
				}
			}

			fmt.Fprintln(w, "ALIAS\tNAME\tGPUs\tCTX\tR\tV\tT\t$/HR\tGGUF REPO\tQUANT")
			fmt.Fprintln(w, "-----\t----\t----\t---\t-\t-\t-\t----\t---------\t-----")
			for _, m := range models {
				numGPUs := m.NumGPUs
				if numGPUs <= 0 {
					numGPUs = 1
				}
				gpuStr := fmt.Sprintf("%dx %dGB", numGPUs, m.VRAM)
				ctxStr := formatContext(m.ContextLength)
				rStr := boolMark(m.Reasoning)
				vStr := boolMark(m.Vision)
				tStr := boolMark(m.ToolCalling)
				priceStr := "-"
				if p, ok := prices[m.Name]; ok {
					priceStr = fmt.Sprintf("$%.3f", p)
				}
				// The empty-quant fallback only means something for a GGUF entry,
				// where llama.cpp resolves a bare -hf to Q4_K_M. Non-llama.cpp
				// entries (ComfyUI, Jupyter) have no repo and no quant at all, and
				// printing a quant tag for them invented a fact.
				quantStr := m.Quant
				if quantStr == "" {
					quantStr = "-"
					if m.HFRepo != "" {
						quantStr = "Q4_K_M" // llama.cpp's default when -hf carries no tag
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Alias, m.Name, gpuStr, ctxStr, rStr, vStr, tStr, priceStr, m.HFRepo, quantStr)
			}
			return w.Flush()
		},
	}
}
