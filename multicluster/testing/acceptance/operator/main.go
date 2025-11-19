package main

import (
	"context"
	"os"

	"github.com/andrewstucki/locking/multicluster"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	log := zerologr.New(&logger)
	ctrl.SetLogger(log)

	cmd := &cobra.Command{
		Use: "operator",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := multicluster.RaftConfigurationFromFlags()
			if err != nil {
				log.Error(err, "configuring raft from command-line")
				os.Exit(1)
			}
			config.Logger = log
			config.Scheme = runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(config.Scheme))

			manager, err := multicluster.NewRaftRuntimeManager(config)
			if err != nil {
				log.Error(err, "initializing cluster")
				os.Exit(1)
			}

			if err := mcbuilder.ControllerManagedBy(manager).For(&corev1.ConfigMap{}, mcbuilder.WithEngageWithLocalCluster(true)).Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName, "namespace", req.Namespace, "name", req.Name)

				cluster, err := manager.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return ctrl.Result{}, err
				}

				client := cluster.GetClient()
				var configmap corev1.ConfigMap
				if err := client.Get(ctx, req.NamespacedName, &configmap); err != nil {
					if apierrors.IsNotFound(err) {
						return ctrl.Result{}, nil
					}
					return ctrl.Result{}, err
				}

				if configmap.Annotations["acceptance.testing/reconcile"] == "true" {
					log.Info("Reconciling ConfigMap")
					if configmap.Data == nil {
						log.Info("Reconciling ConfigMap")
						configmap.Data = map[string]string{}
						log.Info("config map empty")
					} else {
						log.Info("config map value", "value", configmap.Data["reconciled"])
					}

					if configmap.Data["reconciled"] != "true" {
						configmap.Data["reconciled"] = "true"
						if err := client.Update(ctx, &configmap); err != nil {
							if apierrors.IsConflict(err) {
								return ctrl.Result{Requeue: true}, nil
							}
							return ctrl.Result{}, err
						}
					}
				}
				return ctrl.Result{}, nil
			})); err != nil {
				log.Error(err, "initializing controller")
				os.Exit(1)
			}

			if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
				log.Error(err, "running manager")
				os.Exit(1)
			}
		},
	}

	cmd.AddCommand(bootstrapCmd(log))

	multicluster.AddRaftConfigurationFlags(cmd.Flags())

	err := cmd.Execute()
	if err != nil {
		log.Error(err, "executing command")
		os.Exit(1)
	}
}
