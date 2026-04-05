package commands

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewLogCmd(app *application.App, vastaiClient *vastai.Client) *cobra.Command {
	var follow bool
	var useSSH bool

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

			// Try REST API first (works without SSH)
			if !useSSH && !follow {
				logURL, err := vastaiClient.GetInstanceLogs(int(inst.VastaiID))
				if err == nil && logURL != "" {
					fmt.Fprintf(os.Stderr, "Fetching logs from vast.ai...\n")
					client := &http.Client{Timeout: 30 * time.Second}
					resp, err := client.Get(logURL)
					if err == nil {
						defer resp.Body.Close()
						io.Copy(os.Stdout, resp.Body)
						return nil
					}
				}
				// Fall through to SSH
			}

			// SSH fallback
			remote, err := vastaiClient.GetInstance(int(inst.VastaiID))
			if err != nil {
				return fmt.Errorf("fetch instance: %w", err)
			}

			sshHost := remote.SSHHost
			if sshHost == "" {
				sshHost = remote.PublicIPAddr
			}
			sshPort := remote.GetSSHPort()

			tailCmd := "tail -50 /tmp/vllm.log 2>/dev/null || echo 'No vLLM log found'"
			if follow {
				tailCmd = "tail -f /tmp/vllm.log 2>/dev/null || echo 'No vLLM log found'"
			}

			sshCmd := exec.Command("ssh",
				"-o", "StrictHostKeyChecking=no",
				"-o", "ConnectTimeout=10",
				"-p", fmt.Sprintf("%d", sshPort),
				fmt.Sprintf("root@%s", sshHost),
				tailCmd,
			)
			sshCmd.Stdout = os.Stdout
			sshCmd.Stderr = os.Stderr
			return sshCmd.Run()
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output (like tail -f, SSH only)")
	cmd.Flags().BoolVar(&useSSH, "ssh", false, "Force SSH instead of REST API")
	return cmd
}
