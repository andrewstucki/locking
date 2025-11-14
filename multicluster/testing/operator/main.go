package main

import (
	"os"

	"github.com/andrewstucki/locking/multicluster"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	logger := zerolog.New(os.Stderr)
	logger = logger.With().Timestamp().Logger()
	log := zerologr.New(&logger)

	cmd := &cobra.Command{
		Use: "operator",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := multicluster.RaftConfigurationFromFlags()
			if err != nil {
				log.Error(err, "configuring raft from command-line")
				os.Exit(1)
			}
			config.Logger = log

			manager, err := multicluster.NewRaftRuntimeManager(config)
			if err != nil {
				log.Error(err, "initializing cluster")
				os.Exit(1)
			}

			if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
				log.Error(err, "running manager")
				os.Exit(1)
			}
		},
	}

	multicluster.AddRaftConfigurationFlags(cmd.Flags())

	err := cmd.Execute()
	if err != nil {
		log.Error(err, "executing command")
		os.Exit(1)
	}
}
