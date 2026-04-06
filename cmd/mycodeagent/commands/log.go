package commands

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewLogCmd(app *application.App, vastaiClient *vastai.Client) *cobra.Command {
	var tail string

	cmd := &cobra.Command{
		Use:   "log <id>",
		Short: "Fetch vLLM logs from a running instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid instance ID: %s", args[0])
			}

			inst, err := app.Instances.FindByID(id)
			if err != nil {
				return err
			}

			logURL, err := vastaiClient.GetInstanceLogs(int(inst.VastaiID), tail)
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "Requesting logs from vast.ai...\n")
			client := &http.Client{Timeout: 30 * time.Second}

			// S3 upload takes a few seconds; retry until available
			for attempt := 0; attempt < 10; attempt++ {
				if attempt > 0 {
					time.Sleep(2 * time.Second)
				}
				resp, err := client.Get(logURL)
				if err != nil {
					return fmt.Errorf("fetch log URL: %w", err)
				}
				if resp.StatusCode == 200 {
					io.Copy(os.Stdout, resp.Body)
					resp.Body.Close()
					return nil
				}
				resp.Body.Close()
				fmt.Fprintf(os.Stderr, "  waiting for logs...\n")
			}
			return fmt.Errorf("logs not available after retries")
		},
	}

	cmd.Flags().StringVarP(&tail, "tail", "n", "100", "Number of lines to show from end of logs")
	return cmd
}
