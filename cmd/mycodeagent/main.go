package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/WWTLF/mycodeagent/cmd/mycodeagent/commands"
	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/config"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/engine"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/persistence"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/serverprobe"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/ssh"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func main() {
	// Ctrl-C must cancel the command's context rather than kill the process:
	// `init` holds a billing GPU for up to 15 minutes, and only a cancelled
	// context lets DeployService run its teardown. NotifyContext restores the
	// default handler after the first signal, so a second Ctrl-C still force
	// quits — at the cost of leaving the instance running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	db, err := persistence.OpenDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	modelRepo := persistence.NewStaticModelRepository()
	instanceRepo := persistence.NewSQLiteInstanceRepository(db)
	badHostRepo := persistence.NewSQLiteBadHostRepository(db)

	vastaiAdapter := vastai.NewAdapter(cfg.VastaiAPIKey)
	sshAdapter := ssh.NewAdapter()

	llamaEngine := engine.NewLlamaCppEngine()

	deploySvc := service.NewDeployService(
		modelRepo, instanceRepo, badHostRepo,
		vastaiAdapter, sshAdapter, llamaEngine,
		cfg.BasePort, cfg.HFToken,
	)

	modelSvc := service.NewModelService(modelRepo)
	badHostSvc := service.NewBadHostService(badHostRepo)
	probe := serverprobe.New()
	instanceSvc := service.NewInstanceService(instanceRepo, vastaiAdapter, sshAdapter, probe, modelSvc, cfg.BasePort)
	credentialStore := config.NewStore()

	app := application.NewApp(deploySvc, instanceSvc, modelSvc, badHostSvc, credentialStore, cfg.VastaiAPIKey, cfg.HFToken)

	rootCmd := &cobra.Command{
		Use:           "mycodeagent",
		Short:         "Deploy and manage llama.cpp models on vast.ai",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			verbose, _ := cmd.Flags().GetBool("verbose")
			vastaiAdapter.SetVerbose(verbose)
		},
	}
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Log all API requests and responses")

	rootCmd.AddCommand(
		commands.NewLoginCmd(app),
		commands.NewModelsCmd(app),
		commands.NewInitCmd(app),
		commands.NewPsCmd(app),
		commands.NewStopCmd(app),
		commands.NewStartCmd(app),
		commands.NewKillCmd(app),
		commands.NewBudgetCmd(app),
		commands.NewTunnelCmd(app),
		commands.NewLogCmd(app),
		commands.NewInfoCmd(app),
		commands.NewConfigCmd(app),
		commands.NewRestartCmd(app),
		commands.NewHostsCmd(app),
	)

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
