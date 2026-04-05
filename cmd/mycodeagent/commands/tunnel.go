package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/ssh"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func NewTunnelCmd(app *application.App, vastaiClient *vastai.Client, basePort int) *cobra.Command {
	return &cobra.Command{
		Use:   "tunnel <id>",
		Short: "Re-establish SSH tunnel to an existing instance",
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

			// Kill old tunnel if still referenced
			if inst.TunnelPID > 0 {
				ssh.StopTunnel(inst.TunnelPID)
			}

			// Refresh SSH info from vast.ai
			remote, err := vastaiClient.GetInstance(int(inst.VastaiID))
			if err != nil {
				return fmt.Errorf("fetch instance from vast.ai: %w", err)
			}
			if remote.ActualStatus != "running" {
				return fmt.Errorf("instance is %s, not running", remote.ActualStatus)
			}

			sshHost := remote.SSHHost
			if sshHost == "" {
				sshHost = remote.PublicIPAddr
			}
			sshPort := remote.GetSSHPort()
			if sshHost == "" || sshPort == 0 {
				return fmt.Errorf("SSH info not available (host=%s port=%d)", sshHost, sshPort)
			}

			// Wait for SSH
			fmt.Printf("Connecting to %s:%d...\n", sshHost, sshPort)
			if err := ssh.WaitForSSH(sshHost, sshPort, 12); err != nil {
				return fmt.Errorf("SSH not reachable: %w", err)
			}

			// Find free port
			localPort, err := ssh.FindFreePort(basePort)
			if err != nil {
				return err
			}

			// Start tunnel
			fmt.Printf("Starting SSH tunnel on port %d...\n", localPort)
			tunnel, err := ssh.StartTunnel(localPort, sshHost, sshPort)
			if err != nil {
				return fmt.Errorf("start tunnel: %w", err)
			}

			// Update DB
			inst.SSHHost = sshHost
			inst.SSHPort = sshPort
			inst.LocalPort = localPort
			inst.TunnelPID = tunnel.PID
			inst.Status = entity.StatusRunning
			if err := app.Instances.Update(inst); err != nil {
				return fmt.Errorf("update instance: %w", err)
			}

			fmt.Printf("Tunnel established: http://localhost:%d/v1 (PID %d)\n", localPort, tunnel.PID)

			// Verify vLLM is serving
			fmt.Print("Verifying vLLM... ")
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(fmt.Sprintf("http://localhost:%d/v1/models", localPort))
			if err != nil {
				fmt.Println("not responding (vLLM may still be loading)")
				return nil
			}
			defer resp.Body.Close()
			var models struct {
				Data []struct {
					ID          string `json:"id"`
					MaxModelLen int    `json:"max_model_len"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&models); err == nil && len(models.Data) > 0 {
				m := models.Data[0]
				fmt.Printf("OK — %s (context: %d)\n", m.ID, m.MaxModelLen)
			} else {
				fmt.Println("connected but no models loaded")
			}

			return nil
		},
	}
}
