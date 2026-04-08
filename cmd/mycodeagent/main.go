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
	"github.com/WWTLF/mycodeagent/internal/infrastructure/serverprobe"
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

	vastaiAdapter := vastai.NewAdapter(cfg.VastaiAPIKey)
	sshAdapter := ssh.NewAdapter()

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
	modelSvc := service.NewModelService(modelRepo)
	probe := serverprobe.New()
	instanceSvc := service.NewInstanceService(instanceRepo, vastaiAdapter, sshAdapter, probe, modelSvc, cfg.BasePort)
	credentialStore := config.NewStore()

	app := application.NewApp(deploySvc, volumeSvc, instanceSvc, modelSvc, credentialStore, cfg.VastaiAPIKey, cfg.HFToken)

	rootCmd := &cobra.Command{
		Use:           "mycodeagent",
		Short:         "Deploy and manage vLLM models on vast.ai",
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
		commands.NewKillCmd(app),
		commands.NewBudgetCmd(app),
		commands.NewTunnelCmd(app),
		commands.NewLogCmd(app),
		commands.NewInfoCmd(app),
		commands.NewConfigCmd(app),
		commands.NewRestartCmd(app),
		commands.NewVolumeCmd(app),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
