package main

import (
	"fmt"
	"os"

	"github.com/WWTLF/mycodeagent/cmd/mycodeagent/commands"
	"github.com/WWTLF/mycodeagent/internal/application"
	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/config"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/engine"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/persistence"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/ssh"
	"github.com/WWTLF/mycodeagent/internal/infrastructure/vastai"
	"github.com/spf13/cobra"
)

func main() {
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
	volumeRepo := persistence.NewSQLiteVolumeRepository(db)

	app := &application.App{
		Models:    modelRepo,
		Instances: instanceRepo,
		Volumes:   volumeRepo,
	}

	vastaiAdapter := vastai.NewAdapter(cfg.VastaiAPIKey)
	sshAdapter := ssh.NewAdapter()
	vastaiClient := vastai.NewClient(cfg.VastaiAPIKey)

	engines := map[entity.ModelEngine]service.EngineProvider{
		entity.EngineVLLM:     engine.NewVLLMEngine(),
		entity.EngineLMStudio: engine.NewLMStudioEngine(),
	}

	deploySvc := service.NewDeployService(
		modelRepo, instanceRepo, volumeRepo,
		vastaiAdapter, sshAdapter, engines,
		cfg.BasePort, cfg.HFToken,
	)

	volumeSvc := service.NewVolumeService(volumeRepo, vastaiAdapter)

	rootCmd := &cobra.Command{
		Use:           "mycodeagent",
		Short:         "Deploy and manage vLLM models on vast.ai",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			verbose, _ := cmd.Flags().GetBool("verbose")
			vastaiClient.SetVerbose(verbose)
			vastaiAdapter.SetVerbose(verbose)
		},
	}
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Log all API requests and responses")

	rootCmd.AddCommand(
		commands.NewLoginCmd(),
		commands.NewModelsCmd(app),
		commands.NewInitCmd(deploySvc),
		commands.NewPsCmd(app, vastaiClient),
		commands.NewStopCmd(deploySvc),
		commands.NewKillCmd(deploySvc),
		commands.NewBudgetCmd(app),
		commands.NewTunnelCmd(app, vastaiClient, cfg.BasePort),
		commands.NewLogCmd(app, vastaiClient),
		commands.NewInfoCmd(app),
		commands.NewConfigCmd(app),
		commands.NewRestartCmd(deploySvc),
		commands.NewVolumeCmd(volumeSvc),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
