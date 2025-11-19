package main

import (
	"os"

	"github.com/andrewstucki/locking/multicluster/bootstrap"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
)

func bootstrapCmd(log logr.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "bootstrap",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := bootstrap.ConfigurationFromFlags()
			if err != nil {
				log.Error(err, "getting bootstrap configuration")
				os.Exit(1)
			}
			if err := bootstrap.BootstrapKubernetesClusters(cmd.Context(), "operator", config); err != nil {
				log.Error(err, "bootstrap kubernetes clusters")
				os.Exit(1)
			}
			log.Info("bootstrapped kubernetes clusters")
		},
	}

	bootstrap.AddBootstrapConfigurationFlags(cmd.Flags())

	return cmd
}
